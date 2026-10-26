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

package cmd

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mattn/go-ieproxy"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/mc/pkg/httptracer"
	"github.com/minio/mc/pkg/probe"
)

// NewAdminFactory encloses New function with client cache.
func NewAdminFactory() func(config *Config) (*madmin.AdminClient, *probe.Error) {
	var cache clientCache[*madmin.AdminClient]

	// Return New function.
	return func(config *Config) (*madmin.AdminClient, *probe.Error) {
		// Creates a parsed URL.
		targetURL, e := url.Parse(config.HostURL)
		if e != nil {
			return nil, probe.NewError(e)
		}
		hostName := targetURL.Host

		settings, err := snapshotClient(config, hostName, targetURL.Scheme != "http")
		if err != nil {
			return nil, err
		}
		return cache.get(&settings, func() (*madmin.AdminClient, *probe.Error) {
			transport, creds := settings.transportAndCredentials()

			// Not found. Instantiate a new MinIO
			api, e := madmin.NewWithOptions(hostName, &madmin.Options{
				Creds:  creds,
				Secure: settings.key.secure,
			})
			if e != nil {
				return nil, probe.NewError(e)
			}

			// Set custom transport.
			api.SetCustomTransport(transport)

			// Set app info.
			api.SetAppInfo(settings.key.appName, settings.key.appVersion)
			return api, nil
		})
	}
}

// newAdminClient gives a new client interface
func newAdminClient(aliasedURL string) (*madmin.AdminClient, *probe.Error) {
	alias, urlStrFull, aliasCfg, err := expandAlias(aliasedURL)
	if err != nil {
		return nil, err.Trace(aliasedURL)
	}
	// Verify if the aliasedURL is a real URL, fail in those cases
	// indicating the user to add alias.
	if aliasCfg == nil && urlRgx.MatchString(aliasedURL) {
		return nil, errInvalidAliasedURL(aliasedURL).Trace(aliasedURL)
	}

	if aliasCfg == nil {
		return nil, probe.NewError(fmt.Errorf("No valid configuration found for '%s' host alias", urlStrFull))
	}

	s3Config := NewS3Config(alias, urlStrFull, aliasCfg)

	s3Client, err := s3AdminNew(s3Config)
	if err != nil {
		return nil, err.Trace(alias, urlStrFull)
	}
	return s3Client, nil
}

func newAnonymousClient(aliasedURL string) (*madmin.AnonymousClient, *probe.Error) {
	_, urlStrFull, aliasCfg, err := expandAlias(aliasedURL)
	if err != nil {
		return nil, err.Trace(aliasedURL)
	}
	// Verify if the aliasedURL is a real URL, fail in those cases
	// indicating the user to add alias.
	if aliasCfg == nil && urlRgx.MatchString(aliasedURL) {
		return nil, errInvalidAliasedURL(aliasedURL).Trace(aliasedURL)
	}
	if aliasCfg == nil {
		return nil, probe.NewError(fmt.Errorf("No valid configuration found for '%s' host alias", urlStrFull))
	}

	// Creates a parsed URL.
	targetURL, e := url.Parse(urlStrFull)
	if e != nil {
		return nil, probe.NewError(e)
	}

	// By default enable HTTPs.
	useTLS := targetURL.Scheme != "http"

	// Construct an anonymous client
	anonClient, e := madmin.NewAnonymousClient(targetURL.Host, useTLS)
	if e != nil {
		return nil, probe.NewError(e)
	}

	// Set custom transport
	var transport http.RoundTripper = &http.Transport{
		Proxy:       ieproxy.GetProxyFunc(),
		DialContext: newCustomDialContext(&Config{}),
		DialTLSContext: newCustomDialTLSContext(&tls.Config{
			RootCAs:            globalRootCAs,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: globalInsecure,
		}),
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
		// Set this value so that the underlying transport round-tripper
		// doesn't try to auto decode the body of objects with
		// content-encoding set to `gzip`.
		//
		// Refer:
		//    https://golang.org/src/net/http/transport.go?h=roundTrip#L1843
		DisableCompression: true,
	}
	if globalDebug {
		transport = httptracer.GetNewTraceTransport(newTraceV4(), transport)
	}
	anonClient.SetCustomTransport(transport)

	return anonClient, nil
}

// s3AdminNew returns an initialized minioAdmin structure. If debug is enabled,
// it also enables an internal trace transport.
var s3AdminNew = NewAdminFactory()
