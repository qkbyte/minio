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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathutil "path"
	"strings"
	"sync"
	"time"

	"github.com/qkbyte/minio/internal/bucket/lifecycle"
	"github.com/qkbyte/minio/internal/logger"
)

func renameAllBucketMetacache(epPath string) error {
	// Rename all previous `.minio.sys/buckets/<bucketname>/.metacache` to
	// to `.minio.sys/tmp/` for deletion.
	return readDirFn(pathJoin(epPath, minioMetaBucket, bucketMetaPrefix), func(name string, typ os.FileMode) error {
		if typ == os.ModeDir {
			tmpMetacacheOld := pathutil.Join(epPath, minioMetaTmpDeletedBucket, mustGetUUID())
			if err := renameAll(pathJoin(epPath, minioMetaBucket, metacachePrefixForID(name, slashSeparator)),
				tmpMetacacheOld); err != nil && err != errFileNotFound {
				return fmt.Errorf("unable to rename (%s -> %s) %w",
					pathJoin(epPath, minioMetaBucket+metacachePrefixForID(minioMetaBucket, slashSeparator)),
					tmpMetacacheOld,
					osErrToFileErr(err))
			}
		}
		return nil
	})
}

// listPath will return the requested entries.
// If no more entries are in the listing io.EOF is returned,
// otherwise nil or an unexpected error is returned.
// The listPathOptions given will be checked and modified internally.
// Required important fields are Bucket, Prefix, Separator.
// Other important fields are Limit, Marker.
// List ID always derived from the Marker.
func (z *erasureServerPools) listPath(ctx context.Context, o *listPathOptions) (entries metaCacheEntriesSorted, err error) {
	if err := checkListObjsArgs(ctx, o.Bucket, o.Prefix, o.Marker, z); err != nil {
		return entries, err
	}

	// Marker is set validate pre-condition.
	if o.Marker != "" && o.Prefix != "" {
		// Marker not common with prefix is not implemented. Send an empty response
		if !HasPrefix(o.Marker, o.Prefix) {
			return entries, io.EOF
		}
	}

	// With max keys of zero we have reached eof, return right here.
	if o.Limit == 0 {
		return entries, io.EOF
	}

	// For delimiter and prefix as '/' we do not list anything at all
	// along // with the prefix. On a flat namespace with 'prefix'
	// as '/' we don't have any entries, since all the keys are
	// of form 'keyName/...'
	if strings.HasPrefix(o.Prefix, SlashSeparator) {
		return entries, io.EOF
	}

	// If delimiter is slashSeparator we must return directories of
	// the non-recursive scan unless explicitly requested.
	o.IncludeDirectories = o.Separator == slashSeparator
	if (o.Separator == slashSeparator || o.Separator == "") && !o.Recursive {
		o.Recursive = o.Separator != slashSeparator
		o.Separator = slashSeparator
	} else {
		// Default is recursive, if delimiter is set then list non recursive.
		o.Recursive = true
	}

	// Decode and get the optional list id from the marker.
	o.parseMarker()
	o.BaseDir = baseDirFromPrefix(o.Prefix)
	o.Transient = o.Transient || isReservedOrInvalidBucket(o.Bucket, false)
	o.SetFilter()
	if o.Transient {
		o.Create = false
	}

	// We have 2 cases:
	// 1) Cold listing, just list.
	// 2) Returning, but with no id. Start async listing.
	// 3) Returning, with ID, stream from list.
	//
	// If we don't have a list id we must ask the server if it has a cache or create a new.
	if o.ID != "" && !o.Transient {
		// Create or ping with handout...
		rpc := globalNotificationSys.restClientFromHash(pathJoin(o.Bucket, o.Prefix))
		var c *metacache
		if rpc == nil {
			resp := localMetacacheMgr.getBucket(ctx, o.Bucket).findCache(*o)
			c = &resp
		} else {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			c, err = rpc.GetMetacacheListing(rctx, *o)
			cancel()
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// Context is canceled, return at once.
				// request canceled, no entries to return
				return entries, io.EOF
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				o.debugln("listPath: got error", err)
			}
			o.Transient = true
			o.Create = false
			o.ID = mustGetUUID()
		} else {
			if c.fileNotFound {
				// No cache found, no entries found.
				return entries, io.EOF
			}
			if c.status == scanStateError || c.status == scanStateNone {
				o.ID = ""
				o.Create = false
				o.debugln("scan status", c.status, " - waiting a roundtrip to create")
			} else {
				// Continue listing
				o.ID = c.id
				go func(meta metacache) {
					// Continuously update while we wait.
					t := time.NewTicker(metacacheMaxClientWait / 10)
					defer t.Stop()
					select {
					case <-ctx.Done():
						// Request is done, stop updating.
						return
					case <-t.C:
						meta.lastHandout = time.Now()
						if rpc == nil {
							meta, _ = localMetacacheMgr.updateCacheEntry(meta)
						}
						meta, _ = rpc.UpdateMetacacheListing(ctx, meta)
					}
				}(*c)
			}
		}
	}

	if o.ID != "" && !o.Transient {
		// We have an existing list ID, continue streaming.
		if o.Create {
			o.debugln("Creating", o)
			entries, err = z.listAndSave(ctx, o)
			if err == nil || err == io.EOF {
				return entries, err
			}
			entries.truncate(0)
		} else {
			if o.pool < len(z.serverPools) && o.set < len(z.serverPools[o.pool].sets) {
				o.debugln("Resuming", o)
				entries, err = z.serverPools[o.pool].sets[o.set].streamMetadataParts(ctx, *o)
				entries.reuse = true // We read from stream and are not sharing results.
				if err == nil {
					return entries, nil
				}
			} else {
				err = fmt.Errorf("invalid pool/set")
				o.pool, o.set = 0, 0
			}
		}
		if IsErr(err, []error{
			nil,
			context.Canceled,
			context.DeadlineExceeded,
			// io.EOF is expected and should be returned but no need to log it.
			io.EOF,
		}...) {
			// Expected good errors we don't need to return error.
			return entries, err
		}
		entries.truncate(0)
		go func() {
			rpc := globalNotificationSys.restClientFromHash(pathJoin(o.Bucket, o.Prefix))
			if rpc != nil {
				ctx, cancel := context.WithTimeout(GlobalContext, 5*time.Second)
				defer cancel()
				c, err := rpc.GetMetacacheListing(ctx, *o)
				if err == nil {
					c.error = "no longer used"
					c.status = scanStateError
					rpc.UpdateMetacacheListing(ctx, *c)
				}
			}
		}()
		o.ID = ""

		if err != nil {
			logger.LogIf(ctx, fmt.Errorf("Resuming listing from drives failed %w, proceeding to do raw listing", err))
		}
	}

	// Do listing in-place.
	// Create output for our results.
	// Create filter for results.
	o.debugln("Raw List", o)
	filterCh := make(chan metaCacheEntry, o.Limit)
	listCtx, cancelList := context.WithCancel(ctx)
	filteredResults := o.gatherResults(listCtx, filterCh)
	var wg sync.WaitGroup
	wg.Add(1)
	var listErr error

	go func(o listPathOptions) {
		defer wg.Done()
		o.StopDiskAtLimit = true
		listErr = z.listMerged(listCtx, o, filterCh)
		o.debugln("listMerged returned with", listErr)
	}(*o)

	entries, err = filteredResults()
	cancelList()
	wg.Wait()
	if listErr != nil && !errors.Is(listErr, context.Canceled) {
		return entries, listErr
	}
	entries.reuse = true
	truncated := entries.len() > o.Limit || err == nil
	entries.truncate(o.Limit)
	if !o.Transient && truncated {
		if o.ID == "" {
			entries.listID = mustGetUUID()
		} else {
			entries.listID = o.ID
		}
	}
	if !truncated {
		return entries, io.EOF
	}
	return entries, nil
}

// listPath will return the requested entries.
// If no more entries are in the listing io.EOF is returned,
// otherwise nil or an unexpected error is returned.
// The listPathOptions given will be checked and modified internally.
// Required important fields are Bucket, Prefix, Separator.
// Other important fields are Limit, Marker.
// List ID always derived from the Marker.
func (es *erasureSingle) listPath(ctx context.Context, o *listPathOptions) (entries metaCacheEntriesSorted, err error) {
	if err := checkListObjsArgs(ctx, o.Bucket, o.Prefix, o.Marker, es); err != nil {
		return entries, err
	}

	// Marker is set validate pre-condition.
	if o.Marker != "" && o.Prefix != "" {
		// Marker not common with prefix is not implemented. Send an empty response
		if !HasPrefix(o.Marker, o.Prefix) {
			return entries, io.EOF
		}
	}

	// With max keys of zero we have reached eof, return right here.
	if o.Limit == 0 {
		return entries, io.EOF
	}

	// For delimiter and prefix as '/' we do not list anything at all
	// along // with the prefix. On a flat namespace with 'prefix'
	// as '/' we don't have any entries, since all the keys are
	// of form 'keyName/...'
	if strings.HasPrefix(o.Prefix, SlashSeparator) {
		return entries, io.EOF
	}

	// If delimiter is slashSeparator we must return directories of
	// the non-recursive scan unless explicitly requested.
	o.IncludeDirectories = o.Separator == slashSeparator
	if (o.Separator == slashSeparator || o.Separator == "") && !o.Recursive {
		o.Recursive = o.Separator != slashSeparator
		o.Separator = slashSeparator
	} else {
		// Default is recursive, if delimiter is set then list non recursive.
		o.Recursive = true
	}

	// Decode and get the optional list id from the marker.
	o.parseMarker()
	o.BaseDir = baseDirFromPrefix(o.Prefix)
	o.Transient = o.Transient || isReservedOrInvalidBucket(o.Bucket, false)
	o.SetFilter()
	if o.Transient {
		o.Create = false
	}

	// We have 2 cases:
	// 1) Cold listing, just list.
	// 2) Returning, but with no id. Start async listing.
	// 3) Returning, with ID, stream from list.
	//
	// If we don't have a list id we must ask the server if it has a cache or create a new.
	if o.ID != "" && !o.Transient {
		resp := localMetacacheMgr.getBucket(ctx, o.Bucket).findCache(*o)
		c := &resp
		if c.fileNotFound {
			// No cache found, no entries found.
			return entries, io.EOF
		}
		if c.status == scanStateError || c.status == scanStateNone {
			o.ID = ""
			o.Create = false
			o.debugln("scan status", c.status, " - waiting a roundtrip to create")
		} else {
			// Continue listing
			o.ID = c.id
			go func(meta metacache) {
				// Continuously update while we wait.
				t := time.NewTicker(metacacheMaxClientWait / 10)
				defer t.Stop()
				select {
				case <-ctx.Done():
					// Request is done, stop updating.
					return
				case <-t.C:
					meta.lastHandout = time.Now()
					meta, _ = localMetacacheMgr.updateCacheEntry(meta)
				}
			}(*c)
		}

		// We have an existing list ID, continue streaming.
		if o.Create {
			o.debugln("Creating", o)
			entries, err = es.listAndSave(ctx, o)
			if err == nil || err == io.EOF {
				return entries, err
			}
			entries.truncate(0)
		} else {
			o.debugln("Resuming", o)
			entries, err = es.streamMetadataParts(ctx, *o)
			entries.reuse = true // We read from stream and are not sharing results.
			if err == nil {
				return entries, nil
			}
		}
		if IsErr(err, []error{
			nil,
			context.Canceled,
			context.DeadlineExceeded,
			// io.EOF is expected and should be returned but no need to log it.
			io.EOF,
		}...) {
			// Expected good errors we don't need to return error.
			return entries, err
		}
		entries.truncate(0)
		o.ID = ""
		if err != nil {
			logger.LogIf(ctx, fmt.Errorf("Resuming listing from drives failed %w, proceeding to do raw listing", err))
		}
	}

	// Do listing in-place.
	// Create output for our results.
	// Create filter for results.
	o.debugln("Raw List", o)
	filterCh := make(chan metaCacheEntry, o.Limit)
	listCtx, cancelList := context.WithCancel(ctx)
	filteredResults := o.gatherResults(listCtx, filterCh)
	var wg sync.WaitGroup
	wg.Add(1)
	var listErr error

	go func(o listPathOptions) {
		defer wg.Done()
		o.Limit = 0
		listErr = es.listMerged(listCtx, o, filterCh)
		o.debugln("listMerged returned with", listErr)
	}(*o)

	entries, err = filteredResults()
	cancelList()
	wg.Wait()
	if listErr != nil && !errors.Is(listErr, context.Canceled) {
		return entries, listErr
	}
	entries.reuse = true
	truncated := entries.len() > o.Limit || err == nil
	entries.truncate(o.Limit)
	if !o.Transient && truncated {
		if o.ID == "" {
			entries.listID = mustGetUUID()
		} else {
			entries.listID = o.ID
		}
	}
	if !truncated {
		return entries, io.EOF
	}
	return entries, nil
}

// listMerged will list across all sets and return a merged results stream.
// The result channel is closed when no more results are expected.
func (es *erasureSingle) listMerged(ctx context.Context, o listPathOptions, results chan<- metaCacheEntry) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var listErr error
	var inputs []chan metaCacheEntry

	innerResults := make(chan metaCacheEntry, 100)
	inputs = append(inputs, innerResults)

	mu.Lock()
	listCtx, cancelList := context.WithCancel(ctx)
	defer cancelList()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := es.listPathInner(listCtx, o, innerResults)
		mu.Lock()
		defer mu.Unlock()
		listErr = err
	}()
	mu.Unlock()

	// Do lifecycle filtering.
	if o.Lifecycle != nil || o.Replication.Config != nil {
		filterIn := make(chan metaCacheEntry, 10)
		go applyBucketActions(ctx, o, filterIn, results)
		// Replace results.
		results = filterIn
	}

	// Gather results to a single channel.
	err := mergeEntryChannels(ctx, inputs, results, func(existing, other *metaCacheEntry) (replace bool) {
		// Pick object over directory
		if existing.isDir() && !other.isDir() {
			return true
		}
		if !existing.isDir() && other.isDir() {
			return false
		}
		eMeta, err := existing.xlmeta()
		if err != nil {
			return true
		}
		oMeta, err := other.xlmeta()
		if err != nil {
			return false
		}
		// Replace if modtime is newer
		if !oMeta.latestModtime().Equal(oMeta.latestModtime()) {
			return oMeta.latestModtime().After(eMeta.latestModtime())
		}
		// Use NumVersions as a final tiebreaker.
		return len(oMeta.versions) > len(eMeta.versions)
	})

	cancelList()
	wg.Wait()
	if err != nil {
		return err
	}
	if listErr != nil {
		if contextCanceled(ctx) {
			return nil
		}
		if listErr.Error() == io.EOF.Error() {
			return nil
		}
		logger.LogIf(ctx, listErr)
		return listErr
	}
	if contextCanceled(ctx) {
		return ctx.Err()
	}
	return nil
}

// listMerged will list across all sets and return a merged results stream.
// The result channel is closed when no more results are expected.
func (z *erasureServerPools) listMerged(ctx context.Context, o listPathOptions, results chan<- metaCacheEntry) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	allAtEOF := true
	var inputs []chan metaCacheEntry
	mu.Lock()
	// Ask all sets and merge entries.
	listCtx, cancelList := context.WithCancel(ctx)
	defer cancelList()
	for _, pool := range z.serverPools {
		for _, set := range pool.sets {
			wg.Add(1)
			innerResults := make(chan metaCacheEntry, 100)
			inputs = append(inputs, innerResults)
			go func(i int, set *erasureObjects) {
				defer wg.Done()
				err := set.listPath(listCtx, o, innerResults)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					allAtEOF = false
				}
				errs[i] = err
			}(len(errs), set)
			errs = append(errs, nil)
		}
	}
	mu.Unlock()

	// Do lifecycle filtering.
	if o.Lifecycle != nil || o.Replication.Config != nil {
		filterIn := make(chan metaCacheEntry, 10)
		go applyBucketActions(ctx, o, filterIn, results)
		// Replace results.
		results = filterIn
	}

	// Gather results to a single channel.
	err := mergeEntryChannels(ctx, inputs, results, func(existing, other *metaCacheEntry) (replace bool) {
		// Pick object over directory
		if existing.isDir() && !other.isDir() {
			return true
		}
		if !existing.isDir() && other.isDir() {
			return false
		}
		eMeta, err := existing.xlmeta()
		if err != nil {
			return true
		}
		oMeta, err := other.xlmeta()
		if err != nil {
			return false
		}
		// Replace if modtime is newer
		if !oMeta.latestModtime().Equal(oMeta.latestModtime()) {
			return oMeta.latestModtime().After(eMeta.latestModtime())
		}
		// Use NumVersions as a final tiebreaker.
		return len(oMeta.versions) > len(eMeta.versions)
	})

	cancelList()
	wg.Wait()
	if err != nil {
		return err
	}

	if contextCanceled(ctx) {
		return ctx.Err()
	}

	if isAllNotFound(errs) {
		return nil
	}

	for _, err := range errs {
		if err == nil || contextCanceled(ctx) {
			allAtEOF = false
			continue
		}
		if err.Error() == io.EOF.Error() {
			continue
		}
		logger.LogIf(ctx, err)
		return err
	}
	if allAtEOF {
		return io.EOF
	}
	return nil
}

// applyBucketActions applies lifecycle and replication actions on the listing
// It will filter out objects if the most recent version should be deleted by lifecycle.
// Entries that failed replication will be queued if no lifecycle rules got applied.
// out will be closed when there are no more results.
// When 'in' is closed or the context is canceled the
// function closes 'out' and exits.
func applyBucketActions(ctx context.Context, o listPathOptions, in <-chan metaCacheEntry, out chan<- metaCacheEntry) {
	defer close(out)

	vcfg, _ := globalBucketVersioningSys.Get(o.Bucket)
	for {
		var obj metaCacheEntry
		var ok bool
		select {
		case <-ctx.Done():
			return
		case obj, ok = <-in:
			if !ok {
				return
			}
		}

		fi, err := obj.fileInfo(o.Bucket)
		if err != nil {
			continue
		}

		versioned := vcfg != nil && vcfg.Versioned(obj.name)

		objInfo := fi.ToObjectInfo(o.Bucket, obj.name, versioned)
		if o.Lifecycle != nil {
			action := evalActionFromLifecycle(ctx, *o.Lifecycle, o.Retention, objInfo, false)
			switch action {
			case lifecycle.DeleteVersionAction, lifecycle.DeleteAction:
				globalExpiryState.enqueueByDays(objInfo, false, action == lifecycle.DeleteVersionAction)
				// Skip this entry.
				continue
			case lifecycle.DeleteRestoredAction, lifecycle.DeleteRestoredVersionAction:
				globalExpiryState.enqueueByDays(objInfo, true, action == lifecycle.DeleteRestoredVersionAction)
				// Skip this entry.
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case out <- obj:
			queueReplicationHeal(ctx, o.Bucket, objInfo, o.Replication)
		}
	}
}

func (es *erasureSingle) listAndSave(ctx context.Context, o *listPathOptions) (entries metaCacheEntriesSorted, err error) {
	// Use ID as the object name...
	o.pool = 0
	o.set = 0
	saver := es

	// Disconnect from call above, but cancel on exit.
	listCtx, cancel := context.WithCancel(GlobalContext)
	saveCh := make(chan metaCacheEntry, metacacheBlockSize)
	inCh := make(chan metaCacheEntry, metacacheBlockSize)
	outCh := make(chan metaCacheEntry, o.Limit)

	filteredResults := o.gatherResults(ctx, outCh)

	mc := o.newMetacache()
	meta := metaCacheRPC{meta: &mc, cancel: cancel, rpc: globalNotificationSys.restClientFromHash(pathJoin(o.Bucket, o.Prefix)), o: *o}

	// Save listing...
	go func() {
		if err := saver.saveMetaCacheStream(listCtx, &meta, saveCh); err != nil {
			meta.setErr(err.Error())
		}
		cancel()
	}()

	// Do listing...
	go func(o listPathOptions) {
		err := es.listMerged(listCtx, o, inCh)
		if err != nil {
			meta.setErr(err.Error())
		}
		o.debugln("listAndSave: listing", o.ID, "finished with ", err)
	}(*o)

	// Keep track of when we return since we no longer have to send entries to output.
	var funcReturned bool
	var funcReturnedMu sync.Mutex
	defer func() {
		funcReturnedMu.Lock()
		funcReturned = true
		funcReturnedMu.Unlock()
	}()
	// Write listing to results and saver.
	go func() {
		var returned bool
		for entry := range inCh {
			if !returned {
				funcReturnedMu.Lock()
				returned = funcReturned
				funcReturnedMu.Unlock()
				outCh <- entry
				if returned {
					close(outCh)
				}
			}
			entry.reusable = returned
			saveCh <- entry
		}
		if !returned {
			close(outCh)
		}
		close(saveCh)
	}()

	return filteredResults()
}

func (z *erasureServerPools) listAndSave(ctx context.Context, o *listPathOptions) (entries metaCacheEntriesSorted, err error) {
	// Use ID as the object name...
	o.pool = z.getAvailablePoolIdx(ctx, minioMetaBucket, o.ID, 10<<20)
	if o.pool < 0 {
		// No space or similar, don't persist the listing.
		o.pool = 0
		o.Create = false
		o.ID = ""
		o.Transient = true
		return entries, errDiskFull
	}
	o.set = z.serverPools[o.pool].getHashedSetIndex(o.ID)
	saver := z.serverPools[o.pool].sets[o.set]

	// Disconnect from call above, but cancel on exit.
	listCtx, cancel := context.WithCancel(GlobalContext)
	saveCh := make(chan metaCacheEntry, metacacheBlockSize)
	inCh := make(chan metaCacheEntry, metacacheBlockSize)
	outCh := make(chan metaCacheEntry, o.Limit)

	filteredResults := o.gatherResults(ctx, outCh)

	mc := o.newMetacache()
	meta := metaCacheRPC{meta: &mc, cancel: cancel, rpc: globalNotificationSys.restClientFromHash(pathJoin(o.Bucket, o.Prefix)), o: *o}

	// Save listing...
	go func() {
		if err := saver.saveMetaCacheStream(listCtx, &meta, saveCh); err != nil {
			meta.setErr(err.Error())
		}
		cancel()
	}()

	// Do listing...
	go func(o listPathOptions) {
		err := z.listMerged(listCtx, o, inCh)
		if err != nil {
			meta.setErr(err.Error())
		}
		o.debugln("listAndSave: listing", o.ID, "finished with ", err)
	}(*o)

	// Keep track of when we return since we no longer have to send entries to output.
	var funcReturned bool
	var funcReturnedMu sync.Mutex
	defer func() {
		funcReturnedMu.Lock()
		funcReturned = true
		funcReturnedMu.Unlock()
	}()
	// Write listing to results and saver.
	go func() {
		var returned bool
		for entry := range inCh {
			if !returned {
				funcReturnedMu.Lock()
				returned = funcReturned
				funcReturnedMu.Unlock()
				outCh <- entry
				if returned {
					close(outCh)
				}
			}
			entry.reusable = returned
			saveCh <- entry
		}
		if !returned {
			close(outCh)
		}
		close(saveCh)
	}()

	return filteredResults()
}
