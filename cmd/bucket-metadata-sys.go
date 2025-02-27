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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/minio/madmin-go"
	"github.com/minio/minio-go/v7/pkg/tags"
	"github.com/minio/pkg/bucket/policy"
	bucketsse "github.com/qkbyte/minio/internal/bucket/encryption"
	"github.com/qkbyte/minio/internal/bucket/lifecycle"
	objectlock "github.com/qkbyte/minio/internal/bucket/object/lock"
	"github.com/qkbyte/minio/internal/bucket/replication"
	"github.com/qkbyte/minio/internal/bucket/versioning"
	"github.com/qkbyte/minio/internal/event"
	"github.com/qkbyte/minio/internal/kms"
	"github.com/qkbyte/minio/internal/logger"
	"github.com/qkbyte/minio/internal/sync/errgroup"
)

// BucketMetadataSys captures all bucket metadata for a given cluster.
type BucketMetadataSys struct {
	sync.RWMutex
	metadataMap map[string]BucketMetadata
}

// Count returns number of bucket metadata map entries.
func (sys *BucketMetadataSys) Count() int {
	sys.RLock()
	defer sys.RUnlock()

	return len(sys.metadataMap)
}

// Remove bucket metadata from memory.
func (sys *BucketMetadataSys) Remove(bucket string) {
	if globalIsGateway {
		return
	}
	sys.Lock()
	delete(sys.metadataMap, bucket)
	globalBucketMonitor.DeleteBucket(bucket)
	sys.Unlock()
}

// Set - sets a new metadata in-memory.
// Only a shallow copy is saved and fields with references
// cannot be modified without causing a race condition,
// so they should be replaced atomically and not appended to, etc.
// Data is not persisted to disk.
func (sys *BucketMetadataSys) Set(bucket string, meta BucketMetadata) {
	if globalIsGateway {
		return
	}

	if bucket != minioMetaBucket {
		sys.Lock()
		sys.metadataMap[bucket] = meta
		sys.Unlock()
	}
}

// Update update bucket metadata for the specified config file.
// The configData data should not be modified after being sent here.
func (sys *BucketMetadataSys) Update(ctx context.Context, bucket string, configFile string, configData []byte) (updatedAt time.Time, err error) {
	objAPI := newObjectLayerFn()
	if objAPI == nil {
		return updatedAt, errServerNotInitialized
	}

	if globalIsGateway && globalGatewayName != NASBackendGateway {
		if configFile == bucketPolicyConfig {
			if configData == nil {
				return updatedAt, objAPI.DeleteBucketPolicy(ctx, bucket)
			}
			config, err := policy.ParseConfig(bytes.NewReader(configData), bucket)
			if err != nil {
				return updatedAt, err
			}
			return updatedAt, objAPI.SetBucketPolicy(ctx, bucket, config)
		}
		return updatedAt, NotImplemented{}
	}

	if bucket == minioMetaBucket {
		return updatedAt, errInvalidArgument
	}

	meta, err := loadBucketMetadata(ctx, objAPI, bucket)
	if err != nil {
		if !globalIsErasure && !globalIsDistErasure && errors.Is(err, errVolumeNotFound) {
			// Only single drive mode needs this fallback.
			meta = newBucketMetadata(bucket)
		} else {
			return updatedAt, err
		}
	}
	updatedAt = UTCNow()
	switch configFile {
	case bucketPolicyConfig:
		meta.PolicyConfigJSON = configData
		meta.PolicyConfigUpdatedAt = updatedAt
	case bucketNotificationConfig:
		meta.NotificationConfigXML = configData
	case bucketLifecycleConfig:
		meta.LifecycleConfigXML = configData
	case bucketSSEConfig:
		meta.EncryptionConfigXML = configData
		meta.EncryptionConfigUpdatedAt = updatedAt
	case bucketTaggingConfig:
		meta.TaggingConfigXML = configData
		meta.TaggingConfigUpdatedAt = updatedAt
	case bucketQuotaConfigFile:
		meta.QuotaConfigJSON = configData
		meta.QuotaConfigUpdatedAt = updatedAt
	case objectLockConfig:
		meta.ObjectLockConfigXML = configData
		meta.ObjectLockConfigUpdatedAt = updatedAt
	case bucketVersioningConfig:
		meta.VersioningConfigXML = configData
		meta.VersioningConfigUpdatedAt = updatedAt
	case bucketReplicationConfig:
		meta.ReplicationConfigXML = configData
		meta.ReplicationConfigUpdatedAt = updatedAt
	case bucketTargetsFile:
		meta.BucketTargetsConfigJSON, meta.BucketTargetsConfigMetaJSON, err = encryptBucketMetadata(ctx, meta.Name, configData, kms.Context{
			bucket:            meta.Name,
			bucketTargetsFile: bucketTargetsFile,
		})
		if err != nil {
			return updatedAt, fmt.Errorf("Error encrypting bucket target metadata %w", err)
		}
	default:
		return updatedAt, fmt.Errorf("Unknown bucket %s metadata update requested %s", bucket, configFile)
	}

	if err := meta.Save(ctx, objAPI); err != nil {
		return updatedAt, err
	}

	sys.Set(bucket, meta)
	globalNotificationSys.LoadBucketMetadata(bgContext(ctx), bucket) // Do not use caller context here

	return updatedAt, nil
}

// Get metadata for a bucket.
// If no metadata exists errConfigNotFound is returned and a new metadata is returned.
// Only a shallow copy is returned, so referenced data should not be modified,
// but can be replaced atomically.
//
// This function should only be used with
// - GetBucketInfo
// - ListBuckets
// For all other bucket specific metadata, use the relevant
// calls implemented specifically for each of those features.
func (sys *BucketMetadataSys) Get(bucket string) (BucketMetadata, error) {
	if globalIsGateway || bucket == minioMetaBucket {
		return newBucketMetadata(bucket), errConfigNotFound
	}

	sys.RLock()
	defer sys.RUnlock()

	meta, ok := sys.metadataMap[bucket]
	if !ok {
		return newBucketMetadata(bucket), errConfigNotFound
	}

	return meta, nil
}

// GetVersioningConfig returns configured versioning config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetVersioningConfig(bucket string) (*versioning.Versioning, time.Time, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return &versioning.Versioning{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/"}, meta.Created, nil
		}
		return &versioning.Versioning{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/"}, time.Time{}, err
	}
	return meta.versioningConfig, meta.VersioningConfigUpdatedAt, nil
}

// GetTaggingConfig returns configured tagging config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetTaggingConfig(bucket string) (*tags.Tags, time.Time, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketTaggingNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}
	if meta.taggingConfig == nil {
		return nil, time.Time{}, BucketTaggingNotFound{Bucket: bucket}
	}
	return meta.taggingConfig, meta.TaggingConfigUpdatedAt, nil
}

// GetObjectLockConfig returns configured object lock config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetObjectLockConfig(bucket string) (*objectlock.Config, time.Time, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketObjectLockConfigNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}
	if meta.objectLockConfig == nil {
		return nil, time.Time{}, BucketObjectLockConfigNotFound{Bucket: bucket}
	}
	return meta.objectLockConfig, meta.ObjectLockConfigUpdatedAt, nil
}

// GetLifecycleConfig returns configured lifecycle config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetLifecycleConfig(bucket string) (*lifecycle.Lifecycle, error) {
	if globalIsGateway && globalGatewayName == NASBackendGateway {
		// Only needed in case of NAS gateway.
		objAPI := newObjectLayerFn()
		if objAPI == nil {
			return nil, errServerNotInitialized
		}
		meta, err := loadBucketMetadata(GlobalContext, objAPI, bucket)
		if err != nil {
			return nil, err
		}
		if meta.lifecycleConfig == nil {
			return nil, BucketLifecycleNotFound{Bucket: bucket}
		}
		return meta.lifecycleConfig, nil
	}

	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, BucketLifecycleNotFound{Bucket: bucket}
		}
		return nil, err
	}
	if meta.lifecycleConfig == nil {
		return nil, BucketLifecycleNotFound{Bucket: bucket}
	}
	return meta.lifecycleConfig, nil
}

// GetNotificationConfig returns configured notification config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetNotificationConfig(bucket string) (*event.Config, error) {
	if globalIsGateway && globalGatewayName == NASBackendGateway {
		// Only needed in case of NAS gateway.
		objAPI := newObjectLayerFn()
		if objAPI == nil {
			return nil, errServerNotInitialized
		}
		meta, err := loadBucketMetadata(GlobalContext, objAPI, bucket)
		if err != nil {
			return nil, err
		}
		return meta.notificationConfig, nil
	}

	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		return nil, err
	}
	return meta.notificationConfig, nil
}

// GetSSEConfig returns configured SSE config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetSSEConfig(bucket string) (*bucketsse.BucketSSEConfig, time.Time, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketSSEConfigNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}
	if meta.sseConfig == nil {
		return nil, time.Time{}, BucketSSEConfigNotFound{Bucket: bucket}
	}
	return meta.sseConfig, meta.EncryptionConfigUpdatedAt, nil
}

// CreatedAt returns the time of creation of bucket
func (sys *BucketMetadataSys) CreatedAt(bucket string) (time.Time, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		return time.Time{}, err
	}
	return meta.Created.UTC(), nil
}

// GetPolicyConfig returns configured bucket policy
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetPolicyConfig(bucket string) (*policy.Policy, time.Time, error) {
	if globalIsGateway {
		objAPI := newObjectLayerFn()
		if objAPI == nil {
			return nil, time.Time{}, errServerNotInitialized
		}
		p, err := objAPI.GetBucketPolicy(GlobalContext, bucket)
		return p, UTCNow(), err
	}

	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketPolicyNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}
	if meta.policyConfig == nil {
		return nil, time.Time{}, BucketPolicyNotFound{Bucket: bucket}
	}
	return meta.policyConfig, meta.PolicyConfigUpdatedAt, nil
}

// GetQuotaConfig returns configured bucket quota
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetQuotaConfig(ctx context.Context, bucket string) (*madmin.BucketQuota, time.Time, error) {
	meta, err := sys.GetConfig(ctx, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketQuotaConfigNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}
	return meta.quotaConfig, meta.QuotaConfigUpdatedAt, nil
}

// GetReplicationConfig returns configured bucket replication config
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetReplicationConfig(ctx context.Context, bucket string) (*replication.Config, time.Time, error) {
	meta, err := sys.GetConfig(ctx, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, time.Time{}, BucketReplicationConfigNotFound{Bucket: bucket}
		}
		return nil, time.Time{}, err
	}

	if meta.replicationConfig == nil {
		return nil, time.Time{}, BucketReplicationConfigNotFound{Bucket: bucket}
	}
	return meta.replicationConfig, meta.ReplicationConfigUpdatedAt, nil
}

// GetBucketTargetsConfig returns configured bucket targets for this bucket
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetBucketTargetsConfig(bucket string) (*madmin.BucketTargets, error) {
	meta, err := sys.GetConfig(GlobalContext, bucket)
	if err != nil {
		if errors.Is(err, errConfigNotFound) {
			return nil, BucketRemoteTargetNotFound{Bucket: bucket}
		}
		return nil, err
	}
	if meta.bucketTargetConfig == nil {
		return nil, BucketRemoteTargetNotFound{Bucket: bucket}
	}
	return meta.bucketTargetConfig, nil
}

// GetConfig returns a specific configuration from the bucket metadata.
// The returned object may not be modified.
func (sys *BucketMetadataSys) GetConfig(ctx context.Context, bucket string) (BucketMetadata, error) {
	objAPI := newObjectLayerFn()
	if objAPI == nil {
		return newBucketMetadata(bucket), errServerNotInitialized
	}

	if globalIsGateway {
		return newBucketMetadata(bucket), NotImplemented{}
	}

	if bucket == minioMetaBucket {
		return newBucketMetadata(bucket), errInvalidArgument
	}

	sys.RLock()
	meta, ok := sys.metadataMap[bucket]
	sys.RUnlock()
	if ok {
		return meta, nil
	}
	meta, err := loadBucketMetadata(ctx, objAPI, bucket)
	if err != nil {
		return meta, err
	}
	sys.Lock()
	sys.metadataMap[bucket] = meta
	sys.Unlock()

	return meta, nil
}

// Init - initializes bucket metadata system for all buckets.
func (sys *BucketMetadataSys) Init(ctx context.Context, buckets []BucketInfo, objAPI ObjectLayer) error {
	if objAPI == nil {
		return errServerNotInitialized
	}

	// In gateway mode, we don't need to load bucket metadata except
	// NAS gateway backend.
	if globalIsGateway && !objAPI.IsNotificationSupported() {
		return nil
	}

	// Load bucket metadata sys in background
	go sys.load(ctx, buckets, objAPI)
	return nil
}

// concurrently load bucket metadata to speed up loading bucket metadata.
func (sys *BucketMetadataSys) concurrentLoad(ctx context.Context, buckets []BucketInfo, objAPI ObjectLayer) {
	g := errgroup.WithNErrs(len(buckets))
	for index := range buckets {
		index := index
		g.Go(func() error {
			_, _ = objAPI.HealBucket(ctx, buckets[index].Name, madmin.HealOpts{
				// Ensure heal opts for bucket metadata be deep healed all the time.
				ScanMode: madmin.HealDeepScan,
				Recreate: true,
			})
			meta, err := loadBucketMetadata(ctx, objAPI, buckets[index].Name)
			if err != nil {
				if !globalIsErasure && !globalIsDistErasure && errors.Is(err, errVolumeNotFound) {
					meta = newBucketMetadata(buckets[index].Name)
				} else {
					return err
				}
			}
			sys.Lock()
			sys.metadataMap[buckets[index].Name] = meta
			sys.Unlock()

			globalEventNotifier.set(buckets[index], meta) // set notification targets

			globalBucketTargetSys.set(buckets[index], meta) // set remote replication targets

			return nil
		}, index)
	}
	for _, err := range g.Wait() {
		if err != nil {
			logger.LogIf(ctx, err)
		}
	}
}

// Loads bucket metadata for all buckets into BucketMetadataSys.
func (sys *BucketMetadataSys) load(ctx context.Context, buckets []BucketInfo, objAPI ObjectLayer) {
	count := 100 // load 100 bucket metadata at a time.
	for {
		if len(buckets) < count {
			sys.concurrentLoad(ctx, buckets, objAPI)
			return
		}
		sys.concurrentLoad(ctx, buckets[:count], objAPI)
		buckets = buckets[count:]
	}
}

// Reset the state of the BucketMetadataSys.
func (sys *BucketMetadataSys) Reset() {
	sys.Lock()
	for k := range sys.metadataMap {
		delete(sys.metadataMap, k)
	}
	sys.Unlock()
}

// NewBucketMetadataSys - creates new policy system.
func NewBucketMetadataSys() *BucketMetadataSys {
	return &BucketMetadataSys{
		metadataMap: make(map[string]BucketMetadata),
	}
}
