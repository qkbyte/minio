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

package logger

import (
	"context"
	"encoding/hex"
	"fmt"
	"go/build"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/minio/highwayhash"
	"github.com/minio/madmin-go"
	"github.com/minio/minio-go/v7/pkg/set"
	xhttp "github.com/qkbyte/minio/internal/http"
	"github.com/qkbyte/minio/internal/logger/message/log"
)

// HighwayHash key for logging in anonymous mode
var magicHighwayHash256Key = []byte("\x4b\xe7\x34\xfa\x8e\x23\x8a\xcd\x26\x3e\x83\xe6\xbb\x96\x85\x52\x04\x0f\x93\x5d\xa3\x9f\x44\x14\x97\xe0\x9d\x13\x22\xde\x36\xa0")

// LogLevel type
type LogLevel int8

// Enumerated level types
const (
	InfoLvl LogLevel = iota + 1
	ErrorLvl
	FatalLvl

	Application = madmin.LogKindApplication
	Minio       = madmin.LogKindMinio
	All         = madmin.LogKindAll
)

// MinimumLogLevel holds the minimum logging level to print - info by default
var MinimumLogLevel = InfoLvl

var trimStrings []string

// TimeFormat - logging time format.
const TimeFormat string = "15:04:05 MST 01/02/2006"

var matchingFuncNames = [...]string{
	"http.HandlerFunc.ServeHTTP",
	"cmd.serverMain",
	"cmd.StartGateway",
	// add more here ..
}

func (level LogLevel) String() string {
	switch level {
	case InfoLvl:
		return "INFO"
	case ErrorLvl:
		return "ERROR"
	case FatalLvl:
		return "FATAL"
	}
	return ""
}

// quietFlag: Hide startup messages if enabled
// jsonFlag: Display in JSON format, if enabled
var (
	quietFlag, jsonFlag, anonFlag bool
	// Custom function to format error
	errorFmtFunc func(string, error, bool) string
)

// EnableQuiet - turns quiet option on.
func EnableQuiet() {
	quietFlag = true
}

// EnableJSON - outputs logs in json format.
func EnableJSON() {
	jsonFlag = true
	quietFlag = true
}

// EnableAnonymous - turns anonymous flag
// to avoid printing sensitive information.
func EnableAnonymous() {
	anonFlag = true
}

// IsJSON - returns true if jsonFlag is true
func IsJSON() bool {
	return jsonFlag
}

// IsQuiet - returns true if quietFlag is true
func IsQuiet() bool {
	return quietFlag
}

// RegisterError registers the specified rendering function. This latter
// will be called for a pretty rendering of fatal errors.
func RegisterError(f func(string, error, bool) string) {
	errorFmtFunc = f
}

// Remove any duplicates and return unique entries.
func uniqueEntries(paths []string) []string {
	m := make(set.StringSet)
	for _, p := range paths {
		if !m.Contains(p) {
			m.Add(p)
		}
	}
	return m.ToSlice()
}

// Init sets the trimStrings to possible GOPATHs
// and GOROOT directories. Also append github.com/qkbyte/minio
// This is done to clean up the filename, when stack trace is
// displayed when an error happens.
func Init(goPath string, goRoot string) {
	var goPathList []string
	var goRootList []string
	var defaultgoPathList []string
	var defaultgoRootList []string
	pathSeperator := ":"
	// Add all possible GOPATH paths into trimStrings
	// Split GOPATH depending on the OS type
	if runtime.GOOS == "windows" {
		pathSeperator = ";"
	}

	goPathList = strings.Split(goPath, pathSeperator)
	goRootList = strings.Split(goRoot, pathSeperator)
	defaultgoPathList = strings.Split(build.Default.GOPATH, pathSeperator)
	defaultgoRootList = strings.Split(build.Default.GOROOT, pathSeperator)

	// Add trim string "{GOROOT}/src/" into trimStrings
	trimStrings = []string{filepath.Join(runtime.GOROOT(), "src") + string(filepath.Separator)}

	// Add all possible path from GOPATH=path1:path2...:pathN
	// as "{path#}/src/" into trimStrings
	for _, goPathString := range goPathList {
		trimStrings = append(trimStrings, filepath.Join(goPathString, "src")+string(filepath.Separator))
	}

	for _, goRootString := range goRootList {
		trimStrings = append(trimStrings, filepath.Join(goRootString, "src")+string(filepath.Separator))
	}

	for _, defaultgoPathString := range defaultgoPathList {
		trimStrings = append(trimStrings, filepath.Join(defaultgoPathString, "src")+string(filepath.Separator))
	}

	for _, defaultgoRootString := range defaultgoRootList {
		trimStrings = append(trimStrings, filepath.Join(defaultgoRootString, "src")+string(filepath.Separator))
	}

	// Remove duplicate entries.
	trimStrings = uniqueEntries(trimStrings)

	// Add "github.com/qkbyte/minio" as the last to cover
	// paths like "{GOROOT}/src/github.com/qkbyte/minio"
	// and "{GOPATH}/src/github.com/qkbyte/minio"
	trimStrings = append(trimStrings, filepath.Join("github.com", "minio", "minio")+string(filepath.Separator))
}

func trimTrace(f string) string {
	for _, trimString := range trimStrings {
		f = strings.TrimPrefix(filepath.ToSlash(f), filepath.ToSlash(trimString))
	}
	return filepath.FromSlash(f)
}

func getSource(level int) string {
	pc, file, lineNumber, ok := runtime.Caller(level)
	if ok {
		// Clean up the common prefixes
		file = trimTrace(file)
		_, funcName := filepath.Split(runtime.FuncForPC(pc).Name())
		return fmt.Sprintf("%v:%v:%v()", file, lineNumber, funcName)
	}
	return ""
}

// getTrace method - creates and returns stack trace
func getTrace(traceLevel int) []string {
	var trace []string
	pc, file, lineNumber, ok := runtime.Caller(traceLevel)

	for ok && file != "" {
		// Clean up the common prefixes
		file = trimTrace(file)
		// Get the function name
		_, funcName := filepath.Split(runtime.FuncForPC(pc).Name())
		// Skip duplicate traces that start with file name, "<autogenerated>"
		// and also skip traces with function name that starts with "runtime."
		if !strings.HasPrefix(file, "<autogenerated>") &&
			!strings.HasPrefix(funcName, "runtime.") {
			// Form and append a line of stack trace into a
			// collection, 'trace', to build full stack trace
			trace = append(trace, fmt.Sprintf("%v:%v:%v()", file, lineNumber, funcName))

			// Ignore trace logs beyond the following conditions
			for _, name := range matchingFuncNames {
				if funcName == name {
					return trace
				}
			}
		}
		traceLevel++
		// Read stack trace information from PC
		pc, file, lineNumber, ok = runtime.Caller(traceLevel)
	}
	return trace
}

// Return the highway hash of the passed string
func hashString(input string) string {
	hh, _ := highwayhash.New(magicHighwayHash256Key)
	hh.Write([]byte(input))
	return hex.EncodeToString(hh.Sum(nil))
}

// LogAlwaysIf prints a detailed error message during
// the execution of the server.
func LogAlwaysIf(ctx context.Context, err error, errKind ...interface{}) {
	if err == nil {
		return
	}

	logIf(ctx, err, errKind...)
}

// LogIf prints a detailed error message during
// the execution of the server, if it is not an
// ignored error.
func LogIf(ctx context.Context, err error, errKind ...interface{}) {
	if logIgnoreError(err) {
		return
	}
	logIf(ctx, err, errKind...)
}

func errToEntry(ctx context.Context, err error, errKind ...interface{}) log.Entry {
	logKind := madmin.LogKindAll
	if len(errKind) > 0 {
		if ek, ok := errKind[0].(madmin.LogKind); ok {
			logKind = ek
		}
	}
	req := GetReqInfo(ctx)

	if req == nil {
		req = &ReqInfo{API: "SYSTEM"}
	}
	req.RLock()
	defer req.RUnlock()

	API := "SYSTEM"
	if req.API != "" {
		API = req.API
	}

	kv := req.GetTags()
	tags := make(map[string]interface{}, len(kv))
	for _, entry := range kv {
		tags[entry.Key] = entry.Val
	}

	// Get full stack trace
	trace := getTrace(3)

	// Get the cause for the Error
	message := fmt.Sprintf("%v (%T)", err, err)
	if req.DeploymentID == "" {
		req.DeploymentID = xhttp.GlobalDeploymentID
	}

	objects := make([]log.ObjectVersion, 0, len(req.Objects))
	for _, ov := range req.Objects {
		objects = append(objects, log.ObjectVersion{
			ObjectName: ov.ObjectName,
			VersionID:  ov.VersionID,
		})
	}

	entry := log.Entry{
		DeploymentID: req.DeploymentID,
		Level:        ErrorLvl.String(),
		LogKind:      logKind,
		RemoteHost:   req.RemoteHost,
		Host:         req.Host,
		RequestID:    req.RequestID,
		UserAgent:    req.UserAgent,
		Time:         time.Now().UTC(),
		API: &log.API{
			Name: API,
			Args: &log.Args{
				Bucket:    req.BucketName,
				Object:    req.ObjectName,
				VersionID: req.VersionID,
				Objects:   objects,
			},
		},
		Trace: &log.Trace{
			Message:   message,
			Source:    trace,
			Variables: tags,
		},
	}

	if anonFlag {
		entry.API.Args.Bucket = hashString(entry.API.Args.Bucket)
		entry.API.Args.Object = hashString(entry.API.Args.Object)
		entry.RemoteHost = hashString(entry.RemoteHost)
		entry.Trace.Message = reflect.TypeOf(err).String()
		entry.Trace.Variables = make(map[string]interface{})
	}

	return entry
}

// consoleLogIf prints a detailed error message during
// the execution of the server.
func consoleLogIf(ctx context.Context, err error, errKind ...interface{}) {
	if MinimumLogLevel > ErrorLvl {
		return
	}

	if consoleTgt != nil {
		entry := errToEntry(ctx, err, errKind...)
		consoleTgt.Send(entry)
	}
}

// logIf prints a detailed error message during
// the execution of the server.
func logIf(ctx context.Context, err error, errKind ...interface{}) {
	if MinimumLogLevel > ErrorLvl {
		return
	}

	systemTgts := SystemTargets()
	if len(systemTgts) == 0 {
		return
	}

	entry := errToEntry(ctx, err, errKind...)
	// Iterate over all logger targets to send the log entry
	for _, t := range systemTgts {
		if err := t.Send(entry); err != nil {
			if consoleTgt != nil {
				entry.Trace.Message = fmt.Sprintf("event(%#v) was not sent to Logger target (%#v): %#v", entry, t, err)
				consoleTgt.Send(entry)
			}
		}
	}
}

// ErrCritical is the value panic'd whenever CriticalIf is called.
var ErrCritical struct{}

// CriticalIf logs the provided error on the console. It fails the
// current go-routine by causing a `panic(ErrCritical)`.
func CriticalIf(ctx context.Context, err error, errKind ...interface{}) {
	if err != nil {
		LogIf(ctx, err, errKind...)
		panic(ErrCritical)
	}
}

// FatalIf is similar to Fatal() but it ignores passed nil error
func FatalIf(err error, msg string, data ...interface{}) {
	if err == nil {
		return
	}
	fatal(err, msg, data...)
}
