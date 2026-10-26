// Copyright (c) 2026 Feng Ruohang
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"crypto/x509"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/net/http/httpproxy"
)

// Equality compares every field, not just a digest. Keep secrets private and
// redact the whole key when formatting it, including diagnostic %#v output.
type clientCacheKey struct {
	endpoint, region, appName, appVersion string
	accessKey, secretKey, sessionToken    string
	signer                                credentials.SignatureType
	lookup                                minio.BucketLookupType
	secure, trailingHeaders               bool
	provider                              credentialSource
	transport                             transportCacheKey
}

func (clientCacheKey) Format(w fmt.State, _ rune) {
	_, _ = io.WriteString(w, "[redacted client configuration]")
}

type transportCacheKey struct {
	rootCAs                     *x509.CertPool
	proxy                       httpproxy.Config
	tls, insecure, notify       bool
	trace                       string
	readDeadline, writeDeadline time.Duration
	uploadLimit, downloadLimit  int64
}

type transportSettings struct {
	key          transportCacheKey
	rootCAs      *x509.CertPool
	customHeader http.Header
	resolvers    map[string]netip.Addr
	proxy        func(*url.URL) (*url.URL, error)
}

func snapshotTransport(config *Config, useTLS bool) transportSettings {
	// Preserve net/http's uppercase-before-lowercase environment precedence.
	proxyEnv := func(upper, lower string) string {
		if value := os.Getenv(upper); value != "" {
			return value
		}
		return os.Getenv(lower)
	}
	proxyConfig := httpproxy.Config{
		HTTPProxy: proxyEnv("HTTP_PROXY", "http_proxy"), HTTPSProxy: proxyEnv("HTTPS_PROXY", "https_proxy"),
		NoProxy: proxyEnv("NO_PROXY", "no_proxy"), CGI: os.Getenv("REQUEST_METHOD") != "",
	}
	settings := transportSettings{
		key: transportCacheKey{
			rootCAs: globalRootCAs,
			proxy:   proxyConfig,
			tls:     useTLS, insecure: config.Insecure,
			notify:       !config.Debug && !globalJSON && !globalJSONLine,
			readDeadline: config.ConnReadDeadline, writeDeadline: config.ConnWriteDeadline,
			uploadLimit: config.UploadLimit, downloadLimit: config.DownloadLimit,
		},
		rootCAs:      globalRootCAs,
		customHeader: globalCustomHeader,
		resolvers:    globalResolvers,
	}
	if config.Debug && (strings.EqualFold(config.Signature, "s3v4") || strings.EqualFold(config.Signature, "s3v2")) {
		settings.key.trace = strings.ToLower(config.Signature)
	}
	return settings
}

// Clone mutable inputs only when constructing a client. Cache hits compare
// against the retained snapshot without copying certificates or parsing proxy
// rules for each object. Neither live maps nor the live pool escape into a client.
func (s transportSettings) clone() transportSettings {
	if s.rootCAs != nil {
		s.rootCAs = s.rootCAs.Clone()
	}
	s.customHeader = s.customHeader.Clone()
	s.resolvers = maps.Clone(s.resolvers)
	s.proxy = s.key.proxy.ProxyFunc()
	return s
}

type clientSettings struct {
	key       clientCacheKey
	config    Config
	transport transportSettings
}

func snapshotClient(config *Config, endpoint string, secure bool) (clientSettings, *probe.Error) {
	c := *config
	c.Region = resolveRegion(&c)
	source, err := snapshotCredentialSource(c.Alias)
	if err != nil {
		return clientSettings{}, err
	}
	transport := snapshotTransport(&c, secure || source.isTLS())
	signer := credentials.SignatureV4
	if strings.EqualFold(c.Signature, "s3v2") {
		signer = credentials.SignatureV2
	}
	return clientSettings{
		config: c, transport: transport,
		key: clientCacheKey{
			endpoint: endpoint, secure: secure,
			accessKey: c.AccessKey, secretKey: c.SecretKey, sessionToken: c.SessionToken,
			signer: signer, region: c.Region, lookup: c.Lookup,
			appName: c.AppName, appVersion: c.AppVersion,
			trailingHeaders: useTrailingHeaders.Load(),
			provider:        source, transport: transport.key,
		},
	}, nil
}

func (s *clientSettings) transportAndCredentials() (http.RoundTripper, *credentials.Credentials) {
	s.transport = s.transport.clone()
	var credentialTransport http.RoundTripper
	if s.config.Transport == nil && s.config.Debug && s.key.provider.mode != "" {
		// STS error bodies can reflect tokens from POST bodies, which the
		// generic header/query trace redactor cannot recognize.
		c := s.config
		c.Debug = false
		settings := s.transport
		settings.key.notify = false
		c.initTransportWithSettings(false, settings)
		credentialTransport = c.Transport
	}
	if s.config.Transport == nil {
		s.config.initTransportWithSettings(true, s.transport)
	}
	transport := s.config.Transport
	if credentialTransport == nil {
		credentialTransport = transport
	}
	providers := s.key.provider.providers(s.key, credentialTransport)
	return transport, credentials.NewChainCredentials(providers)
}

type cachedClient[T any] struct {
	transport transportSettings
	client    T
}

type clientCache[T any] struct {
	sync.RWMutex
	entries map[clientCacheKey][]cachedClient[T]
}

// lookup is called with the cache's read or write lock held.
func (c *clientCache[T]) lookup(settings *clientSettings) (T, bool) {
	for _, entry := range c.entries[settings.key] {
		// Equal detects additions to the original pool; its pointer in the
		// key also isolates constraint callbacks, which Equal cannot compare.
		if entry.transport.rootCAs.Equal(settings.transport.rootCAs) &&
			maps.EqualFunc(entry.transport.customHeader, settings.transport.customHeader, slices.Equal) &&
			maps.Equal(entry.transport.resolvers, settings.transport.resolvers) {
			return entry.client, true
		}
	}
	var zero T
	return zero, false
}

func (c *clientCache[T]) get(settings *clientSettings, create func() (T, *probe.Error)) (T, *probe.Error) {
	// RoundTrippers may contain maps, slices, closures, or mutable state.
	// Even a comparable interface does not establish configuration equality.
	if settings.config.Transport != nil {
		return create()
	}
	c.RLock()
	client, found := c.lookup(settings)
	c.RUnlock()
	if found {
		return client, nil
	}
	c.Lock()
	defer c.Unlock()
	// Another caller may have filled the cache while this caller waited.
	if client, found := c.lookup(settings); found {
		return client, nil
	}
	client, err := create()
	if err == nil {
		if c.entries == nil {
			c.entries = make(map[clientCacheKey][]cachedClient[T])
		}
		c.entries[settings.key] = append(c.entries[settings.key], cachedClient[T]{
			transport: settings.transport, client: client,
		})
	}
	return client, err
}
