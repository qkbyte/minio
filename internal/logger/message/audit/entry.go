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

package audit

import (
	"net/http"
	"strings"
	"time"

	"github.com/qkbyte/minio/internal/handlers"
	xhttp "github.com/qkbyte/minio/internal/http"
)

// Version - represents the current version of audit log structure.
const Version = "1"

// ObjectVersion object version key/versionId
type ObjectVersion struct {
	ObjectName string `json:"objectName"`
	VersionID  string `json:"versionId,omitempty"`
}

// Entry - audit entry logs.
type Entry struct {
	Version      string    `json:"version"`
	DeploymentID string    `json:"deploymentid,omitempty"`
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	// deprecated replaced by 'Event', kept here for some
	// time for backward compatibility with k8s Operator.
	Trigger string `json:"trigger"`
	API     struct {
		Name            string          `json:"name,omitempty"`
		Bucket          string          `json:"bucket,omitempty"`
		Object          string          `json:"object,omitempty"`
		Objects         []ObjectVersion `json:"objects,omitempty"`
		Status          string          `json:"status,omitempty"`
		StatusCode      int             `json:"statusCode,omitempty"`
		InputBytes      int64           `json:"rx"`
		OutputBytes     int64           `json:"tx"`
		HeaderBytes     int64           `json:"txHeaders,omitempty"`
		TimeToFirstByte string          `json:"timeToFirstByte,omitempty"`
		TimeToResponse  string          `json:"timeToResponse,omitempty"`
	} `json:"api"`
	RemoteHost string                 `json:"remotehost,omitempty"`
	RequestID  string                 `json:"requestID,omitempty"`
	UserAgent  string                 `json:"userAgent,omitempty"`
	ReqClaims  map[string]interface{} `json:"requestClaims,omitempty"`
	ReqQuery   map[string]string      `json:"requestQuery,omitempty"`
	ReqHeader  map[string]string      `json:"requestHeader,omitempty"`
	RespHeader map[string]string      `json:"responseHeader,omitempty"`
	Tags       map[string]interface{} `json:"tags,omitempty"`

	Error string `json:"error,omitempty"`
}

// NewEntry - constructs an audit entry object with some fields filled
func NewEntry(deploymentID string) Entry {
	return Entry{
		Version:      Version,
		DeploymentID: deploymentID,
		Time:         time.Now().UTC(),
	}
}

// ToEntry - constructs an audit entry from a http request
func ToEntry(w http.ResponseWriter, r *http.Request, reqClaims map[string]interface{}, deploymentID string) Entry {
	entry := NewEntry(deploymentID)

	entry.RemoteHost = handlers.GetSourceIP(r)
	entry.UserAgent = r.UserAgent()
	entry.ReqClaims = reqClaims

	q := r.URL.Query()
	reqQuery := make(map[string]string, len(q))
	for k, v := range q {
		reqQuery[k] = strings.Join(v, ",")
	}
	entry.ReqQuery = reqQuery

	reqHeader := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		reqHeader[k] = strings.Join(v, ",")
	}
	entry.ReqHeader = reqHeader

	wh := w.Header()
	entry.RequestID = wh.Get(xhttp.AmzRequestID)
	respHeader := make(map[string]string, len(wh))
	for k, v := range wh {
		respHeader[k] = strings.Join(v, ",")
	}
	entry.RespHeader = respHeader

	if etag := respHeader[xhttp.ETag]; etag != "" {
		respHeader[xhttp.ETag] = strings.Trim(etag, `"`)
	}

	return entry
}
