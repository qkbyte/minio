// Copyright (c) 2015-2022 MinIO, Inc.
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

package openid

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	xnet "github.com/minio/pkg/net"
	"github.com/qkbyte/minio/internal/arn"
	"github.com/qkbyte/minio/internal/config"
	"github.com/qkbyte/minio/internal/config/identity/openid/provider"
	xhttp "github.com/qkbyte/minio/internal/http"
)

type providerCfg struct {
	// Used for user interface like console
	DisplayName string

	JWKS struct {
		URL *xnet.URL
	}
	URL                *xnet.URL
	ClaimPrefix        string
	ClaimName          string
	ClaimUserinfo      bool
	RedirectURI        string
	RedirectURIDynamic bool
	DiscoveryDoc       DiscoveryDoc
	ClientID           string
	ClientSecret       string
	RolePolicy         string

	roleArn  arn.ARN
	provider provider.Provider
}

func newProviderCfgFromConfig(getCfgVal func(cfgName string) string) providerCfg {
	return providerCfg{
		DisplayName:        getCfgVal(DisplayName),
		ClaimName:          getCfgVal(ClaimName),
		ClaimUserinfo:      getCfgVal(ClaimUserinfo) == config.EnableOn,
		ClaimPrefix:        getCfgVal(ClaimPrefix),
		RedirectURI:        getCfgVal(RedirectURI),
		RedirectURIDynamic: getCfgVal(RedirectURIDynamic) == config.EnableOn,
		ClientID:           getCfgVal(ClientID),
		ClientSecret:       getCfgVal(ClientSecret),
		RolePolicy:         getCfgVal(RolePolicy),
	}
}

const (
	keyCloakVendor = "keycloak"
)

// initializeProvider initializes if any additional vendor specific information
// was provided, initialization will return an error initial login fails.
func (p *providerCfg) initializeProvider(cfgGet func(string) string, transport http.RoundTripper) error {
	vendor := cfgGet(Vendor)
	if vendor == "" {
		return nil
	}
	var err error
	switch vendor {
	case keyCloakVendor:
		adminURL := cfgGet(KeyCloakAdminURL)
		realm := cfgGet(KeyCloakRealm)
		p.provider, err = provider.KeyCloak(
			provider.WithAdminURL(adminURL),
			provider.WithOpenIDConfig(provider.DiscoveryDoc(p.DiscoveryDoc)),
			provider.WithTransport(transport),
			provider.WithRealm(realm),
		)
		return err
	default:
		return fmt.Errorf("Unsupport vendor %s", keyCloakVendor)
	}
}

// UserInfo returns claims for authenticated user from userInfo endpoint.
//
// Some OIDC implementations such as GitLab do not support
// claims as part of the normal oauth2 flow, instead rely
// on service providers making calls to IDP to fetch additional
// claims available from the UserInfo endpoint
func (p *providerCfg) UserInfo(accessToken string, transport http.RoundTripper) (map[string]interface{}, error) {
	if p.JWKS.URL == nil || p.JWKS.URL.String() == "" {
		return nil, errors.New("openid not configured")
	}
	client := &http.Client{
		Transport: transport,
	}

	req, err := http.NewRequest(http.MethodPost, p.DiscoveryDoc.UserInfoEndpoint, nil)
	if err != nil {
		return nil, err
	}

	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer xhttp.DrainBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// uncomment this for debugging when needed.
		// reqBytes, _ := httputil.DumpRequest(req, false)
		// fmt.Println(string(reqBytes))
		// respBytes, _ := httputil.DumpResponse(resp, true)
		// fmt.Println(string(respBytes))
		return nil, errors.New(resp.Status)
	}

	dec := json.NewDecoder(resp.Body)
	claims := map[string]interface{}{}

	if err = dec.Decode(&claims); err != nil {
		// uncomment this for debugging when needed.
		// reqBytes, _ := httputil.DumpRequest(req, false)
		// fmt.Println(string(reqBytes))
		// respBytes, _ := httputil.DumpResponse(resp, true)
		// fmt.Println(string(respBytes))
		return nil, err
	}

	return claims, nil
}
