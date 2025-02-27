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
	"fmt"
	"strings"

	"github.com/qkbyte/minio/internal/color"
	"github.com/qkbyte/minio/internal/logger"
)

// Prints the formatted startup message.
func printGatewayStartupMessage(apiEndPoints []string, backendType string) {
	if len(globalSubnetConfig.APIKey) == 0 {
		var builder strings.Builder
		startupBanner(&builder)
		logger.Info("\n" + builder.String())
	}

	strippedAPIEndpoints := stripStandardPorts(apiEndPoints, globalMinioHost)
	// If cache layer is enabled, print cache capacity.
	cacheAPI := newCachedObjectLayerFn()
	if cacheAPI != nil {
		printCacheStorageInfo(cacheAPI.StorageInfo(GlobalContext))
	}
	// Prints credential.
	printGatewayCommonMsg(strippedAPIEndpoints)

	// Prints `mc` cli configuration message chooses
	// first endpoint as default.
	printCLIAccessMsg(strippedAPIEndpoints[0], fmt.Sprintf("my%s", backendType))

	// Prints documentation message.
	printObjectAPIMsg()
}

// Prints common server startup message. Prints credential, region and browser access.
func printGatewayCommonMsg(apiEndpoints []string) {
	// Get saved credentials.
	cred := globalActiveCred

	apiEndpointStr := strings.Join(apiEndpoints, "  ")

	// Colorize the message and print.
	logger.Info(color.Blue("API: ") + color.Bold(fmt.Sprintf("%s ", apiEndpointStr)))
	if color.IsTerminal() && !globalCLIContext.Anonymous {
		logger.Info(color.Blue("RootUser: ") + color.Bold(fmt.Sprintf("%s ", cred.AccessKey)))
		logger.Info(color.Blue("RootPass: ") + color.Bold(fmt.Sprintf("%s ", cred.SecretKey)))
	}
	printEventNotifiers()

	if globalBrowserEnabled {
		consoleEndpointStr := strings.Join(stripStandardPorts(getConsoleEndpoints(), globalMinioConsoleHost), " ")
		logger.Info(color.Blue("\nConsole: ") + color.Bold(fmt.Sprintf("%s ", consoleEndpointStr)))
		if color.IsTerminal() && !globalCLIContext.Anonymous {
			logger.Info(color.Blue("RootUser: ") + color.Bold(fmt.Sprintf("%s ", cred.AccessKey)))
			logger.Info(color.Blue("RootPass: ") + color.Bold(fmt.Sprintf("%s ", cred.SecretKey)))
		}
	}
}
