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
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	jsoniter "github.com/json-iterator/go"
	miniogopolicy "github.com/minio/minio-go/v7/pkg/policy"
	"github.com/minio/minio-go/v7/pkg/tags"
	"github.com/minio/pkg/bucket/policy"
	"github.com/qkbyte/minio/internal/handlers"
	xhttp "github.com/qkbyte/minio/internal/http"
	"github.com/qkbyte/minio/internal/logger"
)

// PolicySys - policy subsystem.
type PolicySys struct{}

// Get returns stored bucket policy
func (sys *PolicySys) Get(bucket string) (*policy.Policy, error) {
	policy, _, err := globalBucketMetadataSys.GetPolicyConfig(bucket)
	return policy, err
}

// IsAllowed - checks given policy args is allowed to continue the Rest API.
func (sys *PolicySys) IsAllowed(args policy.Args) bool {
	p, err := sys.Get(args.BucketName)
	if err == nil {
		return p.IsAllowed(args)
	}

	// Log unhandled errors.
	if _, ok := err.(BucketPolicyNotFound); !ok {
		logger.LogIf(GlobalContext, err)
	}

	// As policy is not available for given bucket name, returns IsOwner i.e.
	// operation is allowed only for owner.
	return args.IsOwner
}

// NewPolicySys - creates new policy system.
func NewPolicySys() *PolicySys {
	return &PolicySys{}
}

func getConditionValues(r *http.Request, lc string, username string, claims map[string]interface{}) map[string][]string {
	currTime := UTCNow()

	principalType := "Anonymous"
	if username != "" {
		principalType = "User"
		if len(claims) > 0 {
			principalType = "AssumedRole"
		}
		if username == globalActiveCred.AccessKey {
			principalType = "Account"
		}
	}

	vid := r.Form.Get(xhttp.VersionID)
	if vid == "" {
		if u, err := url.Parse(r.Header.Get(xhttp.AmzCopySource)); err == nil {
			vid = u.Query().Get(xhttp.VersionID)
		}
	}

	authType := getRequestAuthType(r)
	var signatureVersion string
	switch authType {
	case authTypeSignedV2, authTypePresignedV2:
		signatureVersion = signV2Algorithm
	case authTypeSigned, authTypePresigned, authTypeStreamingSigned, authTypePostPolicy:
		signatureVersion = signV4Algorithm
	}

	var authtype string
	switch authType {
	case authTypePresignedV2, authTypePresigned:
		authtype = "REST-QUERY-STRING"
	case authTypeSignedV2, authTypeSigned, authTypeStreamingSigned:
		authtype = "REST-HEADER"
	case authTypePostPolicy:
		authtype = "POST"
	}

	args := map[string][]string{
		"CurrentTime":      {currTime.Format(time.RFC3339)},
		"EpochTime":        {strconv.FormatInt(currTime.Unix(), 10)},
		"SecureTransport":  {strconv.FormatBool(r.TLS != nil)},
		"SourceIp":         {handlers.GetSourceIP(r)},
		"UserAgent":        {r.UserAgent()},
		"Referer":          {r.Referer()},
		"principaltype":    {principalType},
		"userid":           {username},
		"username":         {username},
		"versionid":        {vid},
		"signatureversion": {signatureVersion},
		"authType":         {authtype},
	}

	if lc != "" {
		args["LocationConstraint"] = []string{lc}
	}

	cloneHeader := r.Header.Clone()

	if userTags := cloneHeader.Get(xhttp.AmzObjectTagging); userTags != "" {
		tag, _ := tags.ParseObjectTags(userTags)
		if tag != nil {
			tagMap := tag.ToMap()
			keys := make([]string, 0, len(tagMap))
			for k, v := range tagMap {
				args[pathJoin("ExistingObjectTag", k)] = []string{v}
				args[pathJoin("RequestObjectTag", k)] = []string{v}
				keys = append(keys, k)
			}
			args["RequestObjectTagKeys"] = keys
		}
	}

	for _, objLock := range []string{
		xhttp.AmzObjectLockMode,
		xhttp.AmzObjectLockLegalHold,
		xhttp.AmzObjectLockRetainUntilDate,
	} {
		if values, ok := cloneHeader[objLock]; ok {
			args[strings.TrimPrefix(objLock, "X-Amz-")] = values
		}
		cloneHeader.Del(objLock)
	}

	for key, values := range cloneHeader {
		if strings.EqualFold(key, xhttp.AmzObjectTagging) {
			continue
		}
		if existingValues, found := args[key]; found {
			args[key] = append(existingValues, values...)
		} else {
			args[key] = values
		}
	}

	cloneURLValues := make(url.Values, len(r.Form))
	for k, v := range r.Form {
		cloneURLValues[k] = v
	}

	for _, objLock := range []string{
		xhttp.AmzObjectLockMode,
		xhttp.AmzObjectLockLegalHold,
		xhttp.AmzObjectLockRetainUntilDate,
	} {
		if values, ok := cloneURLValues[objLock]; ok {
			args[strings.TrimPrefix(objLock, "X-Amz-")] = values
		}
		cloneURLValues.Del(objLock)
	}

	for key, values := range cloneURLValues {
		if existingValues, found := args[key]; found {
			args[key] = append(existingValues, values...)
		} else {
			args[key] = values
		}
	}

	// JWT specific values
	//
	// Add all string claims
	for k, v := range claims {
		vStr, ok := v.(string)
		if ok {
			// Special case for AD/LDAP STS users
			switch k {
			case ldapUser:
				args["user"] = []string{vStr}
			case ldapUserN:
				args["username"] = []string{vStr}
			default:
				args[k] = []string{vStr}
			}
		}
	}
	// Add groups claim which could be a list. This will ensure that the claim
	// `jwt:groups` works.
	if grpsVal, ok := claims["groups"]; ok {
		if grpsIs, ok := grpsVal.([]interface{}); ok {
			grps := []string{}
			for _, gI := range grpsIs {
				if g, ok := gI.(string); ok {
					grps = append(grps, g)
				}
			}
			if len(grps) > 0 {
				args["groups"] = grps
			}
		}
	}

	return args
}

// PolicyToBucketAccessPolicy converts a MinIO policy into a minio-go policy data structure.
func PolicyToBucketAccessPolicy(bucketPolicy *policy.Policy) (*miniogopolicy.BucketAccessPolicy, error) {
	// Return empty BucketAccessPolicy for empty bucket policy.
	if bucketPolicy == nil {
		return &miniogopolicy.BucketAccessPolicy{Version: policy.DefaultVersion}, nil
	}

	data, err := json.Marshal(bucketPolicy)
	if err != nil {
		// This should not happen because bucketPolicy is valid to convert to JSON data.
		return nil, err
	}

	var policyInfo miniogopolicy.BucketAccessPolicy
	json := jsoniter.ConfigCompatibleWithStandardLibrary
	if err = json.Unmarshal(data, &policyInfo); err != nil {
		// This should not happen because data is valid to JSON data.
		return nil, err
	}

	return &policyInfo, nil
}

// BucketAccessPolicyToPolicy - converts minio-go/policy.BucketAccessPolicy to policy.Policy.
func BucketAccessPolicyToPolicy(policyInfo *miniogopolicy.BucketAccessPolicy) (*policy.Policy, error) {
	data, err := json.Marshal(policyInfo)
	if err != nil {
		// This should not happen because policyInfo is valid to convert to JSON data.
		return nil, err
	}

	var bucketPolicy policy.Policy
	json := jsoniter.ConfigCompatibleWithStandardLibrary
	if err = json.Unmarshal(data, &bucketPolicy); err != nil {
		// This should not happen because data is valid to JSON data.
		return nil, err
	}

	return &bucketPolicy, nil
}
