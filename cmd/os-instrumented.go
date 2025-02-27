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
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/madmin-go"
	"github.com/qkbyte/minio/internal/disk"
	ioutilx "github.com/qkbyte/minio/internal/ioutil"
)

//go:generate stringer -type=osMetric -trimprefix=osMetric $GOFILE

type osMetric uint8

const (
	osMetricRemoveAll osMetric = iota
	osMetricMkdirAll
	osMetricMkdir
	osMetricRename
	osMetricOpenFileW
	osMetricOpenFileR
	osMetricOpen
	osMetricOpenFileDirectIO
	osMetricLstat
	osMetricRemove
	osMetricStat
	osMetricAccess
	osMetricCreate
	osMetricReadDirent
	osMetricFdatasync
	osMetricSync
	// .... add more

	osMetricLast
)

var globalOSMetrics osMetrics

func init() {
	// Inject metrics.
	ioutilx.OsOpenFile = OpenFile
	ioutilx.OpenFileDirectIO = OpenFileDirectIO
	ioutilx.OsOpen = Open
}

type osMetrics struct {
	// All fields must be accessed atomically and aligned.
	operations [osMetricLast]uint64
	latency    [osMetricLast]lockedLastMinuteLatency
}

// time an os action.
func (o *osMetrics) time(s osMetric) func() {
	startTime := time.Now()
	return func() {
		duration := time.Since(startTime)

		atomic.AddUint64(&o.operations[s], 1)
		o.latency[s].add(duration)
	}
}

// incTime will increment time on metric s with a specific duration.
func (o *osMetrics) incTime(s osMetric, d time.Duration) {
	atomic.AddUint64(&o.operations[s], 1)
	o.latency[s].add(d)
}

func osTrace(s osMetric, startTime time.Time, duration time.Duration, path string) madmin.TraceInfo {
	return madmin.TraceInfo{
		TraceType: madmin.TraceOS,
		Time:      startTime,
		NodeName:  globalLocalNodeName,
		FuncName:  "os." + s.String(),
		Duration:  duration,
		Path:      path,
	}
}

func updateOSMetrics(s osMetric, paths ...string) func() {
	if globalTrace.NumSubscribers(madmin.TraceOS) == 0 {
		return globalOSMetrics.time(s)
	}

	startTime := time.Now()
	return func() {
		duration := time.Since(startTime)
		globalOSMetrics.incTime(s, duration)
		globalTrace.Publish(osTrace(s, startTime, duration, strings.Join(paths, " -> ")))
	}
}

// RemoveAll captures time taken to call the underlying os.RemoveAll
func RemoveAll(dirPath string) error {
	defer updateOSMetrics(osMetricRemoveAll, dirPath)()
	return os.RemoveAll(dirPath)
}

// Mkdir captures time taken to call os.Mkdir
func Mkdir(dirPath string, mode os.FileMode) error {
	defer updateOSMetrics(osMetricMkdir, dirPath)()
	return os.Mkdir(dirPath, mode)
}

// MkdirAll captures time taken to call os.MkdirAll
func MkdirAll(dirPath string, mode os.FileMode) error {
	defer updateOSMetrics(osMetricMkdirAll, dirPath)()
	return osMkdirAll(dirPath, mode)
}

// Rename captures time taken to call os.Rename
func Rename(src, dst string) error {
	defer updateOSMetrics(osMetricRename, src, dst)()
	return os.Rename(src, dst)
}

// OpenFile captures time taken to call os.OpenFile
func OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	switch flag & writeMode {
	case writeMode:
		defer updateOSMetrics(osMetricOpenFileW, name)()
	default:
		defer updateOSMetrics(osMetricOpenFileR, name)()
	}
	return os.OpenFile(name, flag, perm)
}

// Access captures time taken to call syscall.Access()
// on windows, plan9 and solaris syscall.Access uses
// os.Lstat()
func Access(name string) error {
	defer updateOSMetrics(osMetricAccess, name)()
	return access(name)
}

// Open captures time taken to call os.Open
func Open(name string) (*os.File, error) {
	defer updateOSMetrics(osMetricOpen, name)()
	return os.Open(name)
}

// OpenFileDirectIO captures time taken to call disk.OpenFileDirectIO
func OpenFileDirectIO(name string, flag int, perm os.FileMode) (*os.File, error) {
	defer updateOSMetrics(osMetricOpenFileDirectIO, name)()
	return disk.OpenFileDirectIO(name, flag, perm)
}

// Lstat captures time taken to call os.Lstat
func Lstat(name string) (os.FileInfo, error) {
	defer updateOSMetrics(osMetricLstat, name)()
	return os.Lstat(name)
}

// Remove captures time taken to call os.Remove
func Remove(deletePath string) error {
	defer updateOSMetrics(osMetricRemove, deletePath)()
	return os.Remove(deletePath)
}

// Stat captures time taken to call os.Stat
func Stat(name string) (os.FileInfo, error) {
	defer updateOSMetrics(osMetricStat, name)()
	return os.Stat(name)
}

// Create captures time taken to call os.Create
func Create(name string) (*os.File, error) {
	defer updateOSMetrics(osMetricCreate, name)()
	return os.Create(name)
}

// Fdatasync captures time taken to call Fdatasync
func Fdatasync(f *os.File) error {
	fn := ""
	if f != nil {
		fn = f.Name()
	}
	defer updateOSMetrics(osMetricFdatasync, fn)()
	return disk.Fdatasync(f)
}

// report returns all os metrics.
func (o *osMetrics) report() madmin.OSMetrics {
	var m madmin.OSMetrics
	m.CollectedAt = time.Now()
	m.LifeTimeOps = make(map[string]uint64, osMetricLast)
	for i := osMetric(0); i < osMetricLast; i++ {
		if n := atomic.LoadUint64(&o.operations[i]); n > 0 {
			m.LifeTimeOps[i.String()] = n
		}
	}
	if len(m.LifeTimeOps) == 0 {
		m.LifeTimeOps = nil
	}

	m.LastMinute.Operations = make(map[string]madmin.TimedAction, osMetricLast)
	for i := osMetric(0); i < osMetricLast; i++ {
		lm := o.latency[i].total()
		if lm.N > 0 {
			m.LastMinute.Operations[i.String()] = lm.asTimedAction()
		}
	}
	if len(m.LastMinute.Operations) == 0 {
		m.LastMinute.Operations = nil
	}

	return m
}
