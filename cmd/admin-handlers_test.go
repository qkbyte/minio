// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/minio/madmin-go"
	"github.com/qkbyte/minio/internal/auth"
)

// adminErasureTestBed - encapsulates subsystems that need to be setup for
// admin-handler unit tests.
type adminErasureTestBed struct {
	erasureDirs []string
	objLayer    ObjectLayer
	router      *mux.Router
	done        context.CancelFunc
}

// prepareAdminErasureTestBed - helper function that setups a single-node
// Erasure backend for admin-handler tests.
func prepareAdminErasureTestBed(ctx context.Context) (*adminErasureTestBed, error) {
	ctx, cancel := context.WithCancel(ctx)

	// reset global variables to start afresh.
	resetTestGlobals()

	// Set globalIsErasure to indicate that the setup uses an erasure
	// code backend.
	globalIsErasure = true

	// Initializing objectLayer for HealFormatHandler.
	objLayer, erasureDirs, xlErr := initTestErasureObjLayer(ctx)
	if xlErr != nil {
		cancel()
		return nil, xlErr
	}

	// Initialize minio server config.
	if err := newTestConfig(globalMinioDefaultRegion, objLayer); err != nil {
		cancel()
		return nil, err
	}

	// Initialize boot time
	globalBootTime = UTCNow()

	globalEndpoints = mustGetPoolEndpoints(erasureDirs...)

	initAllSubsystems()

	initConfigSubsystem(ctx, objLayer)

	globalIAMSys.Init(ctx, objLayer, globalEtcdClient, 2*time.Second)

	// Setup admin mgmt REST API handlers.
	adminRouter := mux.NewRouter()
	registerAdminRouter(adminRouter, true)

	return &adminErasureTestBed{
		erasureDirs: erasureDirs,
		objLayer:    objLayer,
		router:      adminRouter,
		done:        cancel,
	}, nil
}

// TearDown - method that resets the test bed for subsequent unit
// tests to start afresh.
func (atb *adminErasureTestBed) TearDown() {
	atb.done()
	removeRoots(atb.erasureDirs)
	resetTestGlobals()
}

// initTestObjLayer - Helper function to initialize an Erasure-based object
// layer and set globalObjectAPI.
func initTestErasureObjLayer(ctx context.Context) (ObjectLayer, []string, error) {
	erasureDirs, err := getRandomDisks(16)
	if err != nil {
		return nil, nil, err
	}
	endpoints := mustGetPoolEndpoints(erasureDirs...)
	globalPolicySys = NewPolicySys()
	objLayer, err := newErasureServerPools(ctx, endpoints)
	if err != nil {
		return nil, nil, err
	}

	// Make objLayer available to all internal services via globalObjectAPI.
	globalObjLayerMutex.Lock()
	globalObjectAPI = objLayer
	globalObjLayerMutex.Unlock()
	return objLayer, erasureDirs, nil
}

// cmdType - Represents different service subcomands like status, stop
// and restart.
type cmdType int

const (
	restartCmd cmdType = iota
	stopCmd
)

// toServiceSignal - Helper function that translates a given cmdType
// value to its corresponding serviceSignal value.
func (c cmdType) toServiceSignal() serviceSignal {
	switch c {
	case restartCmd:
		return serviceRestart
	case stopCmd:
		return serviceStop
	}
	return serviceRestart
}

func (c cmdType) toServiceAction() madmin.ServiceAction {
	switch c {
	case restartCmd:
		return madmin.ServiceActionRestart
	case stopCmd:
		return madmin.ServiceActionStop
	}
	return madmin.ServiceActionRestart
}

// testServiceSignalReceiver - Helper function that simulates a
// go-routine waiting on service signal.
func testServiceSignalReceiver(cmd cmdType, t *testing.T) {
	expectedCmd := cmd.toServiceSignal()
	serviceCmd := <-globalServiceSignalCh
	if serviceCmd != expectedCmd {
		t.Errorf("Expected service command %v but received %v", expectedCmd, serviceCmd)
	}
}

// getServiceCmdRequest - Constructs a management REST API request for service
// subcommands for a given cmdType value.
func getServiceCmdRequest(cmd cmdType, cred auth.Credentials) (*http.Request, error) {
	queryVal := url.Values{}
	queryVal.Set("action", string(cmd.toServiceAction()))
	resource := adminPathPrefix + adminAPIVersionPrefix + "/service?" + queryVal.Encode()
	req, err := newTestRequest(http.MethodPost, resource, 0, nil)
	if err != nil {
		return nil, err
	}

	// management REST API uses signature V4 for authentication.
	err = signRequestV4(req, cred.AccessKey, cred.SecretKey)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// testServicesCmdHandler - parametrizes service subcommand tests on
// cmdType value.
func testServicesCmdHandler(cmd cmdType, t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	adminTestBed, err := prepareAdminErasureTestBed(ctx)
	if err != nil {
		t.Fatal("Failed to initialize a single node Erasure backend for admin handler tests.", err)
	}
	defer adminTestBed.TearDown()

	// Initialize admin peers to make admin RPC calls. Note: In a
	// single node setup, this degenerates to a simple function
	// call under the hood.
	globalMinioAddr = "127.0.0.1:9000"

	var wg sync.WaitGroup

	// Setting up a go routine to simulate ServerRouter's
	// handleServiceSignals for stop and restart commands.
	if cmd == restartCmd {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testServiceSignalReceiver(cmd, t)
		}()
	}
	credentials := globalActiveCred

	req, err := getServiceCmdRequest(cmd, credentials)
	if err != nil {
		t.Fatalf("Failed to build service status request %v", err)
	}

	rec := httptest.NewRecorder()
	adminTestBed.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		resp, _ := io.ReadAll(rec.Body)
		t.Errorf("Expected to receive %d status code but received %d. Body (%s)",
			http.StatusOK, rec.Code, string(resp))
	}

	// Wait until testServiceSignalReceiver() called in a goroutine quits.
	wg.Wait()
}

// Test for service restart management REST API.
func TestServiceRestartHandler(t *testing.T) {
	testServicesCmdHandler(restartCmd, t)
}

// buildAdminRequest - helper function to build an admin API request.
func buildAdminRequest(queryVal url.Values, method, path string,
	contentLength int64, bodySeeker io.ReadSeeker) (*http.Request, error,
) {
	req, err := newTestRequest(method,
		adminPathPrefix+adminAPIVersionPrefix+path+"?"+queryVal.Encode(),
		contentLength, bodySeeker)
	if err != nil {
		return nil, err
	}

	cred := globalActiveCred
	err = signRequestV4(req, cred.AccessKey, cred.SecretKey)
	if err != nil {
		return nil, err
	}

	return req, nil
}

func TestAdminServerInfo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	adminTestBed, err := prepareAdminErasureTestBed(ctx)
	if err != nil {
		t.Fatal("Failed to initialize a single node Erasure backend for admin handler tests.", err)
	}

	defer adminTestBed.TearDown()

	// Initialize admin peers to make admin RPC calls.
	globalMinioAddr = "127.0.0.1:9000"

	// Prepare query params for set-config mgmt REST API.
	queryVal := url.Values{}
	queryVal.Set("info", "")

	req, err := buildAdminRequest(queryVal, http.MethodGet, "/info", 0, nil)
	if err != nil {
		t.Fatalf("Failed to construct get-config object request - %v", err)
	}

	rec := httptest.NewRecorder()
	adminTestBed.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected to succeed but failed with %d", rec.Code)
	}

	results := madmin.InfoMessage{}
	err = json.NewDecoder(rec.Body).Decode(&results)
	if err != nil {
		t.Fatalf("Failed to decode set config result json %v", err)
	}

	if results.Region != globalMinioDefaultRegion {
		t.Errorf("Expected %s, got %s", globalMinioDefaultRegion, results.Region)
	}
}

// TestToAdminAPIErrCode - test for toAdminAPIErrCode helper function.
func TestToAdminAPIErrCode(t *testing.T) {
	testCases := []struct {
		err            error
		expectedAPIErr APIErrorCode
	}{
		// 1. Server not in quorum.
		{
			err:            errErasureWriteQuorum,
			expectedAPIErr: ErrAdminConfigNoQuorum,
		},
		// 2. No error.
		{
			err:            nil,
			expectedAPIErr: ErrNone,
		},
		// 3. Non-admin API specific error.
		{
			err:            errDiskNotFound,
			expectedAPIErr: toAPIErrorCode(GlobalContext, errDiskNotFound),
		},
	}

	for i, test := range testCases {
		actualErr := toAdminAPIErrCode(GlobalContext, test.err)
		if actualErr != test.expectedAPIErr {
			t.Errorf("Test %d: Expected %v but received %v",
				i+1, test.expectedAPIErr, actualErr)
		}
	}
}

func TestExtractHealInitParams(t *testing.T) {
	mkParams := func(clientToken string, forceStart, forceStop bool) url.Values {
		v := url.Values{}
		if clientToken != "" {
			v.Add(mgmtClientToken, clientToken)
		}
		if forceStart {
			v.Add(mgmtForceStart, "")
		}
		if forceStop {
			v.Add(mgmtForceStop, "")
		}
		return v
	}
	qParmsArr := []url.Values{
		// Invalid cases
		mkParams("", true, true),
		mkParams("111", true, true),
		mkParams("111", true, false),
		mkParams("111", false, true),
		// Valid cases follow
		mkParams("", true, false),
		mkParams("", false, true),
		mkParams("", false, false),
		mkParams("111", false, false),
	}
	varsArr := []map[string]string{
		// Invalid cases
		{mgmtPrefix: "objprefix"},
		// Valid cases
		{},
		{mgmtBucket: "bucket"},
		{mgmtBucket: "bucket", mgmtPrefix: "objprefix"},
	}

	// Body is always valid - we do not test JSON decoding.
	body := `{"recursive": false, "dryRun": true, "remove": false, "scanMode": 0}`

	// Test all combinations!
	for pIdx, parms := range qParmsArr {
		for vIdx, vars := range varsArr {
			_, err := extractHealInitParams(vars, parms, bytes.NewReader([]byte(body)))
			isErrCase := false
			if pIdx < 4 || vIdx < 1 {
				isErrCase = true
			}

			if err != ErrNone && !isErrCase {
				t.Errorf("Got unexpected error: %v %v %v", pIdx, vIdx, err)
			} else if err == ErrNone && isErrCase {
				t.Errorf("Got no error but expected one: %v %v", pIdx, vIdx)
			}
		}
	}
}
