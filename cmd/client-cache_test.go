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
	"context"
	"crypto/x509"
	"errors"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7"
)

type cacheTestClient struct {
	api     any
	request func(context.Context) error
}

type cacheSliceTransport []string

func (tr cacheSliceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Request: r,
		Body: io.NopCloser(strings.NewReader(tr[0])),
	}, nil
}

type cacheMapTransport map[string]string

func (tr cacheMapTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return cacheSliceTransport{tr["body"]}.RoundTrip(r)
}

type cacheFuncTransport func(*http.Request) (*http.Response, error)

func (tr cacheFuncTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return tr(r)
}

func TestClientCacheCustomTransport(t *testing.T) {
	isolateCacheTestEnv(t)
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			body := `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`
			if admin {
				body = `{"servers":[]}`
			}
			for _, tr := range []http.RoundTripper{
				cacheSliceTransport{body},
				cacheMapTransport{"body": body},
				cacheFuncTransport(cacheSliceTransport{body}.RoundTrip),
			} {
				factory := cacheTestFactory(admin)
				config := cacheTestConfig()
				config.Transport = tr
				first, err := factory(&config)
				if err != nil {
					t.Fatal(err)
				}
				second, err := factory(&config)
				if err != nil {
					t.Fatal(err)
				}
				if first.api == second.api {
					t.Error("opaque custom transports must bypass shared caching")
				}
				for _, client := range []cacheTestClient{first, second} {
					if err := client.request(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestClientCacheGlobalTransportSettings(t *testing.T) {
	isolateCacheTestEnv(t)
	roots, headers, resolvers := globalRootCAs, globalCustomHeader, globalResolvers
	json, jsonLine, trailers := globalJSON, globalJSONLine, useTrailingHeaders.Load()
	t.Cleanup(func() {
		globalRootCAs, globalCustomHeader, globalResolvers = roots, headers, resolvers
		globalJSON, globalJSONLine = json, jsonLine
		useTrailingHeaders.Store(trailers)
	})
	reset := func() {
		globalRootCAs, globalCustomHeader, globalResolvers = nil, nil, nil
		globalJSON, globalJSONLine = false, false
		useTrailingHeaders.Store(false)
	}
	changes := map[string]func(){
		"roots":   func() { globalRootCAs = x509.NewCertPool() },
		"headers": func() { globalCustomHeader = http.Header{"X-Cache-Test": {"different"}} },
		"resolver": func() {
			globalResolvers = map[string]netip.Addr{"cache.identity.test:9000": netip.MustParseAddr("127.0.0.1")}
		},
		"json":      func() { globalJSON = true },
		"json line": func() { globalJSONLine = true },
		"trailers":  func() { useTrailingHeaders.Store(true) },
	}
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			for setting, change := range changes {
				t.Run(setting, func(t *testing.T) {
					reset()
					factory := cacheTestFactory(admin)
					config := cacheTestConfig()
					first, err := factory(&config)
					if err != nil {
						t.Fatal(err)
					}
					change()
					second, err := factory(&config)
					if err != nil {
						t.Fatal(err)
					}
					if first.api == second.api {
						t.Error("changed transport configuration reused a client")
					}
				})
			}
		})
	}
}

func TestClientCacheTransportSnapshot(t *testing.T) {
	isolateCacheTestEnv(t)
	roots, headers, resolvers := globalRootCAs, globalCustomHeader, globalResolvers
	t.Cleanup(func() {
		globalRootCAs, globalCustomHeader, globalResolvers = roots, headers, resolvers
	})
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			seen := make(chan string, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("X-Cache-Test")
				if admin {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				} else {
					_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
				}
			}))
			defer server.Close()
			pool := x509.NewCertPool()
			pool.AddCert(server.Certificate())
			globalRootCAs = pool
			globalCustomHeader = http.Header{"X-Cache-Test": {"before"}}
			u, _ := url.Parse(server.URL)
			globalResolvers = map[string]netip.Addr{u.Host: netip.MustParseAddr("127.0.0.1")}
			factory := cacheTestFactory(admin)
			config := cacheTestConfig()
			config.HostURL = server.URL
			client, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			// Change every global before the first network request.
			globalRootCAs = x509.NewCertPool()
			globalCustomHeader.Set("X-Cache-Test", "after")
			globalResolvers[u.Host] = netip.MustParseAddr("127.0.0.2")
			if err := client.request(t.Context()); err != nil {
				t.Fatal(err)
			}
			if <-seen != "before" {
				t.Error("cached transport reread modified global headers")
			}
			// An equivalent request still uses the endpoint's original snapshot.
			if err := client.request(t.Context()); err != nil {
				t.Fatal(err)
			}
			if <-seen != "before" {
				t.Error("cached transport changed on reuse")
			}
		})
	}
}

func TestClientCacheRootPoolChanges(t *testing.T) {
	isolateCacheTestEnv(t)
	roots := globalRootCAs
	t.Cleanup(func() { globalRootCAs = roots })
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			globalRootCAs = x509.NewCertPool()
			config := cacheTestConfig()
			config.HostURL = server.URL
			factory := cacheTestFactory(admin)
			before, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			globalRootCAs.AddCert(server.Certificate())
			after, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			if before.api == after.api {
				t.Error("root pool mutation reused the previous client")
			}
			// CertPool.Equal alone cannot distinguish constraint callbacks.
			pool := x509.NewCertPool()
			pool.AddCertWithConstraint(server.Certificate(), func([]*x509.Certificate) error {
				return errors.New("rejected by test constraint")
			})
			globalRootCAs = pool
			constrained, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			if constrained.api == after.api {
				t.Error("different root constraints reused a client")
			}
		})
	}
}

func TestClientCacheProxySnapshot(t *testing.T) {
	isolateCacheTestEnv(t)
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "no_proxy", "NO_PROXY", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Host != "cache.identity.test:9000" {
					t.Error("proxy received the wrong endpoint")
				}
				if admin {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				} else {
					_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
				}
			}))
			defer proxy.Close()
			t.Setenv("HTTP_PROXY", proxy.URL)
			config := cacheTestConfig()
			factory := cacheTestFactory(admin)
			first, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HTTP_PROXY", "http://changed-proxy.invalid")
			second, err := factory(&config)
			if err != nil {
				t.Fatal(err)
			}
			if first.api == second.api {
				t.Error("proxy settings reused a client")
			}
			if err := first.request(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Error("client did not retain its captured proxy")
			}
		})
	}
}

func cacheTestFactory(admin bool) func(*Config) (cacheTestClient, *probe.Error) {
	if admin {
		factory := NewAdminFactory()
		return func(c *Config) (cacheTestClient, *probe.Error) {
			api, err := factory(c)
			return cacheTestClient{api, func(ctx context.Context) error {
				_, err := api.ServerInfo(ctx)
				return err
			}}, err
		}
	}
	factory := newFactory()
	return func(c *Config) (cacheTestClient, *probe.Error) {
		client, err := factory(c)
		if err != nil {
			return cacheTestClient{}, err
		}
		api := client.(*S3Client).api
		return cacheTestClient{api, func(ctx context.Context) error {
			_, err := api.ListBuckets(ctx)
			return err
		}}, nil
	}
}

func cacheTestConfig() Config {
	return Config{
		HostURL: "http://cache.identity.test:9000", AccessKey: "cache-access",
		SecretKey: "cache-test-secret", Signature: "S3v4", Region: "us-east-1",
	}
}

func isolateCacheTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MC_REGION", "AWS_REGION", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"MC_STS_ENDPOINT_", "MC_STS_ENDPOINT_cacheA", "MC_STS_ENDPOINT_cacheB", "MC_STS_ENDPOINT_another-static-alias",
		"MC_WEB_IDENTITY_TOKEN_FILE_cacheA", "MC_WEB_IDENTITY_TOKEN_FILE_cacheB",
		"MC_ROLE_ARN_cacheA", "MC_ROLE_ARN_cacheB", "MC_ROLE_SESSION_NAME_cacheA", "MC_ROLE_SESSION_NAME_cacheB",
	} {
		t.Setenv(key, "")
	}
}

func oldCacheHash(c Config) uint32 {
	hash := fnv.New32a()
	_, _ = io.WriteString(hash, newClientURL(c.HostURL).Host+c.AccessKey+c.SecretKey+c.SessionToken+resolveRegion(&c))
	return hash.Sum32()
}

func TestClientCacheCredentials(t *testing.T) {
	isolateCacheTestEnv(t)
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			for _, collision := range []string{"concatenation", "fnv32"} {
				t.Run(collision, func(t *testing.T) {
					a, b := cacheTestConfig(), cacheTestConfig()
					switch collision {
					case "concatenation":
						a.AccessKey, a.SecretKey = "CACHEA", "bcache-test-secret"
						b.AccessKey, b.SecretKey = "CACHEAb", "cache-test-secret"
					case "fnv32":
						// Synthetic fixed, equal-length inputs with a collision in the old key.
						a.AccessKey, b.AccessKey = "CACHE372169ad2d3d8e7e", "CACHE0b1126fb8b874e90"
					}
					if oldCacheHash(a) != oldCacheHash(b) {
						t.Fatal("fixture must collide in the old cache")
					}
					factory := cacheTestFactory(admin)
					first, err := factory(&a)
					if err != nil {
						t.Fatal(err)
					}
					second, err := factory(&b)
					if err != nil {
						t.Fatal(err)
					}
					if first.api == second.api {
						t.Error("different credentials reused one client")
					}
				})
			}
		})
	}
}

func TestClientCacheRequestIdentity(t *testing.T) {
	isolateCacheTestEnv(t)
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			seen := make(chan http.Header, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				if admin {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				} else {
					_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
				}
			}))
			defer server.Close()
			a := cacheTestConfig()
			a.HostURL, a.AccessKey, a.SecretKey = server.URL, "CACHEA", "bcache-test-secret"
			b := a
			b.AccessKey, b.SecretKey = "CACHEAb", "cache-test-secret"
			factory := cacheTestFactory(admin)
			for _, conf := range []*Config{&a, &b, &a, &b} {
				client, err := factory(conf)
				if err != nil {
					t.Fatal(err)
				}
				if err := client.request(t.Context()); err != nil {
					t.Fatal(err)
				}
				header := <-seen
				if !strings.Contains(header.Get("Authorization"), "Credential="+conf.AccessKey+"/") {
					t.Error("request signed with another configuration's identity")
				}
			}
		})
	}
}

func TestClientCacheConfiguration(t *testing.T) {
	isolateCacheTestEnv(t)
	changes := map[string]func(*Config){
		"scheme":       func(c *Config) { c.HostURL = "https://cache.identity.test:9000" },
		"signature":    func(c *Config) { c.Signature = "S3v2" },
		"lookup":       func(c *Config) { c.Lookup = minio.BucketLookupPath },
		"token":        func(c *Config) { c.SessionToken = "cache-token" },
		"secret":       func(c *Config) { c.SecretKey += "2" },
		"region":       func(c *Config) { c.Region = "eu-west-1" },
		"app":          func(c *Config) { c.AppName = "cache-test" },
		"version":      func(c *Config) { c.AppVersion = "2" },
		"insecure":     func(c *Config) { c.Insecure = true },
		"debug":        func(c *Config) { c.Debug = true },
		"read":         func(c *Config) { c.ConnReadDeadline = time.Second },
		"write":        func(c *Config) { c.ConnWriteDeadline = time.Second },
		"upload":       func(c *Config) { c.UploadLimit = 12345 },
		"download":     func(c *Config) { c.DownloadLimit = 54321 },
		"sts endpoint": func(c *Config) { c.Alias = "cacheB" },
		"transport":    func(c *Config) { c.Transport = &http.Transport{} },
	}
	t.Setenv("MC_STS_ENDPOINT_cacheB", "http://sts.cache.test")
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			for field, change := range changes {
				t.Run(field, func(t *testing.T) {
					a, b := cacheTestConfig(), cacheTestConfig()
					change(&b)
					factory := cacheTestFactory(admin)
					first, err := factory(&a)
					if err != nil {
						t.Fatal(err)
					}
					second, err := factory(&b)
					if err != nil {
						t.Fatal(err)
					}
					if first.api == second.api {
						t.Error("different construction settings reused one client")
					}
				})
			}
		})
	}
}

func TestClientCacheReuseAndConcurrency(t *testing.T) {
	isolateCacheTestEnv(t)
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			factory := cacheTestFactory(admin)
			config := cacheTestConfig()
			const count = 24
			results := make(chan cacheTestClient, count)
			var wg sync.WaitGroup
			for range count {
				wg.Go(func() {
					client, err := factory(&config)
					if err != nil {
						t.Error(err)
						return
					}
					results <- client
				})
			}
			wg.Wait()
			close(results)
			var first any
			for client := range results {
				if first == nil {
					first = client.api
				} else if first != client.api {
					t.Error("identical configuration did not reuse its client")
				}
			}
			if config.Transport != nil {
				t.Error("factory mutated the caller's configuration")
			}
			equivalent := config
			equivalent.Alias = "another-static-alias"
			equivalent.HostURL += "/another/bucket/path"
			equivalent.Signature = "s3v4"
			client, err := factory(&equivalent)
			if err != nil {
				t.Fatal(err)
			}
			if client.api != first {
				t.Error("equivalent endpoint configuration did not reuse its client")
			}
		})
	}
}

func TestClientCacheSTSAliasIdentity(t *testing.T) {
	isolateCacheTestEnv(t)
	dir := t.TempDir()
	for _, alias := range []string{"cacheA", "cacheB", "global"} {
		path := filepath.Join(dir, alias)
		if err := os.WriteFile(path, []byte(alias), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", filepath.Join(dir, "global"))
	t.Setenv("AWS_ROLE_ARN", "global-role")
	t.Setenv("AWS_ROLE_SESSION_NAME", "global-session")
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				alias := r.Form.Get("WebIdentityToken")
				if r.Form.Get("RoleArn") != alias+"-role" || r.Form.Get("RoleSessionName") != alias+"-session" {
					t.Error("STS request mixed role or session settings")
				}
				// Expired credentials force retrieval on the next operation.
				_, _ = io.WriteString(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>`+alias+`</AccessKeyId><SecretAccessKey>test-sts-secret</SecretAccessKey><SessionToken>test-sts-token</SessionToken><Expiration>2000-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
			}))
			defer sts.Close()
			for _, alias := range []string{"cacheA", "cacheB"} {
				t.Setenv("MC_STS_ENDPOINT_"+alias, sts.URL)
				t.Setenv("MC_WEB_IDENTITY_TOKEN_FILE_"+alias, filepath.Join(dir, alias))
				t.Setenv("MC_ROLE_ARN_"+alias, alias+"-role")
				t.Setenv("MC_ROLE_SESSION_NAME_"+alias, alias+"-session")
			}
			seen := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("Authorization")
				if admin {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				} else {
					_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
				}
			}))
			defer server.Close()
			factory := cacheTestFactory(admin)
			clients := make(map[string]cacheTestClient)
			for _, alias := range []string{"cacheA", "cacheB"} {
				conf := cacheTestConfig()
				conf.Alias, conf.HostURL = alias, server.URL
				client, err := factory(&conf)
				if err != nil {
					t.Fatal(err)
				}
				clients[alias] = client
			}
			// Both clients already exist when either provider first retrieves or refreshes.
			for _, alias := range []string{"cacheA", "cacheB", "cacheA", "cacheB"} {
				if err := clients[alias].request(t.Context()); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(<-seen, "Credential="+alias+"/") {
					t.Error("STS client signed with another alias's identity")
				}
			}
			if os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != filepath.Join(dir, "global") ||
				os.Getenv("AWS_ROLE_ARN") != "global-role" || os.Getenv("AWS_ROLE_SESSION_NAME") != "global-session" {
				t.Error("constructing an alias changed the AWS environment")
			}
		})
	}
}

func TestClientCacheEquivalentTransportMaps(t *testing.T) {
	isolateCacheTestEnv(t)
	headers, resolvers := globalCustomHeader, globalResolvers
	t.Cleanup(func() { globalCustomHeader, globalResolvers = headers, resolvers })
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			globalCustomHeader = http.Header{"X-First": {"one", "two"}, "X-Second": {"three"}}
			globalResolvers = map[string]netip.Addr{"cache.identity.test:9000": netip.MustParseAddr("127.0.0.1")}
			factory := cacheTestFactory(admin)
			config := cacheTestConfig()
			get := func() any {
				t.Helper()
				client, err := factory(&config)
				if err != nil {
					t.Fatal(err)
				}
				return client.api
			}
			first := get()
			// Map identity and insertion order must not affect connection reuse.
			globalCustomHeader = http.Header{"X-Second": {"three"}, "X-First": {"one", "two"}}
			globalResolvers = map[string]netip.Addr{"cache.identity.test:9000": netip.MustParseAddr("127.0.0.1")}
			if get() != first {
				t.Fatal("equivalent maps did not reuse the client")
			}
			globalCustomHeader["X-First"][0] = "changed"
			if get() == first {
				t.Fatal("in-place header mutation reused the old client")
			}
			globalCustomHeader["X-First"][0] = "one"
			if get() != first {
				t.Fatal("header snapshot changed with its original map")
			}
			globalResolvers["cache.identity.test:9000"] = netip.MustParseAddr("127.0.0.2")
			if get() == first {
				t.Fatal("in-place resolver mutation reused the old client")
			}
			globalResolvers["cache.identity.test:9000"] = netip.MustParseAddr("127.0.0.1")
			if get() != first {
				t.Fatal("resolver snapshot changed with its original map")
			}
			globalCustomHeader, globalResolvers = nil, nil
			empty := get()
			globalCustomHeader, globalResolvers = http.Header{}, map[string]netip.Addr{}
			if get() != empty {
				t.Fatal("nil and empty maps did not reuse the client")
			}
		})
	}
}
