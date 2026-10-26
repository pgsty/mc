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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

func credentialTestProvider(t *testing.T, alias string) *configuredIAM {
	t.Helper()
	source, err := snapshotCredentialSource(alias)
	if err != nil {
		t.Fatal(err)
	}
	return &configuredIAM{source: source, client: http.DefaultClient}
}

func writeCredentialToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRoleCredentials(w http.ResponseWriter, accessKey string) {
	_, _ = fmt.Fprintf(w, `{"AccessKeyId":%q,"SecretAccessKey":"fixture-secret","Token":"fixture-session","Expiration":"2000-01-01T00:00:00Z","Code":"Success"}`, accessKey)
}

func TestConfiguredIAMWebIdentityRefresh(t *testing.T) {
	isolateCacheTestEnv(t)
	tokenPath := filepath.Join(t.TempDir(), "token")
	writeCredentialToken(t, tokenPath, "first")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("RoleArn") != "original-role" || r.Form.Get("RoleSessionName") != "original-session" {
			t.Error("web identity refresh changed its role or session")
		}
		_, _ = fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>fixture-secret</SecretAccessKey><SessionToken>fixture-session</SessionToken><Expiration>2000-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, r.Form.Get("WebIdentityToken"))
	}))
	defer server.Close()
	t.Setenv("MC_STS_ENDPOINT_cacheA", server.URL)
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenPath)
	t.Setenv("AWS_ROLE_ARN", "original-role")
	t.Setenv("AWS_ROLE_SESSION_NAME", "original-session")
	p := credentialTestProvider(t, "cacheA")
	creds := credentials.New(p)
	// These changes affect future clients, never this provider or its refresh.
	t.Setenv("MC_STS_ENDPOINT_cacheA", "http://unused.invalid")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("AWS_ROLE_ARN", "another-role")
	t.Setenv("AWS_ROLE_SESSION_NAME", "another-session")
	for _, want := range []string{"first", "rotated"} {
		writeCredentialToken(t, tokenPath, want)
		value, err := creds.GetWithContext(&credentials.CredContext{Context: t.Context()})
		if err != nil {
			t.Fatal(err)
		}
		if value.AccessKeyID != want || !p.IsExpired() || value.SessionToken != "fixture-session" {
			t.Error("provider did not refresh using its original token file")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two refreshes, got %d", calls.Load())
	}
}

func TestClientCacheSTSTransportSnapshot(t *testing.T) {
	isolateCacheTestEnv(t)
	roots, headers, resolvers := globalRootCAs, globalCustomHeader, globalResolvers
	t.Cleanup(func() {
		globalRootCAs, globalCustomHeader, globalResolvers = roots, headers, resolvers
	})
	tokenPath := filepath.Join(t.TempDir(), "token")
	writeCredentialToken(t, tokenPath, "web-token")
	sts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cache-Test") != "before" {
			t.Error("STS transport did not retain its header snapshot")
		}
		_, _ = io.WriteString(w, `<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>sts-access</AccessKeyId><SecretAccessKey>fixture-secret</SecretAccessKey><SessionToken>fixture-session</SessionToken><Expiration>2000-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
	}))
	defer sts.Close()
	t.Setenv("MC_STS_ENDPOINT_cacheA", sts.URL)
	t.Setenv("MC_WEB_IDENTITY_TOKEN_FILE_cacheA", tokenPath)
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.Header.Get("Authorization"), "Credential=sts-access/") {
					t.Error("client did not retrieve credentials over its captured STS transport")
				}
				if admin {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				} else {
					_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
				}
			}))
			defer server.Close()
			globalRootCAs = x509.NewCertPool()
			globalRootCAs.AddCert(sts.Certificate())
			globalCustomHeader = http.Header{"X-Cache-Test": {"before"}}
			u, _ := url.Parse(sts.URL)
			globalResolvers = map[string]netip.Addr{u.Host: netip.MustParseAddr("127.0.0.1")}
			config := cacheTestConfig()
			config.Alias, config.HostURL = "cacheA", server.URL
			client, err := cacheTestFactory(admin)(&config)
			if err != nil {
				t.Fatal(err)
			}
			globalRootCAs = x509.NewCertPool()
			globalCustomHeader.Set("X-Cache-Test", "after")
			globalResolvers[u.Host] = netip.MustParseAddr("127.0.0.2")
			t.Setenv("MC_STS_ENDPOINT_cacheA", "http://changed.invalid")
			if err := client.request(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfiguredIAMContainerRefresh(t *testing.T) {
	for _, mode := range []string{"relative", "full", "token-file"} {
		t.Run(mode, func(t *testing.T) {
			isolateCacheTestEnv(t)
			tokenPath := filepath.Join(t.TempDir(), "token")
			writeCredentialToken(t, tokenPath, "file-first")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet {
					t.Error("container provider changed its retrieval method")
				}
				writeRoleCredentials(w, r.Header.Get("Authorization"))
			}))
			defer server.Close()
			t.Setenv("MC_STS_ENDPOINT_cacheA", server.URL)
			t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "direct-token")
			switch mode {
			case "relative":
				t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/ignored-by-endpoint")
			case "full":
				t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://ignored.invalid")
			case "token-file":
				t.Setenv("MC_STS_ENDPOINT_cacheA", "http://overridden.invalid")
				t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", server.URL)
				t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", tokenPath)
			}
			creds := credentials.New(credentialTestProvider(t, "cacheA"))
			t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://changed.invalid")
			t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "changed-token")
			t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "/missing")
			t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/another-provider")
			for _, token := range []string{"file-first", "file-rotated"} {
				writeCredentialToken(t, tokenPath, token)
				value, err := creds.GetWithContext(&credentials.CredContext{Context: t.Context()})
				if err != nil {
					t.Fatal(err)
				}
				want := "direct-token"
				if mode == "token-file" {
					want = token
				}
				if value.AccessKeyID != want {
					t.Error("container retrieval used the wrong authorization token")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("expected two refreshes, got %d", calls.Load())
			}
		})
	}
}

func TestConfiguredIAMInstanceMetadata(t *testing.T) {
	for _, mode := range []string{"v2", "timeout", "timeout-body", "cancel", "deadline", "refused"} {
		t.Run(mode, func(t *testing.T) {
			isolateCacheTestEnv(t)
			var puts, gets atomic.Int32
			requested := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts.Add(1)
					if r.URL.Path != credentials.TokenPath || r.Header.Get(credentials.TokenRequestTTLHeader) != credentials.TokenTTL {
						t.Error("invalid IMDSv2 token request")
					}
					close(requested)
					switch mode {
					case "timeout-body":
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					case "timeout", "cancel", "deadline":
						<-r.Context().Done()
						return
					case "refused":
						w.WriteHeader(http.StatusForbidden)
						return
					}
					_, _ = io.WriteString(w, "imds-token")
					return
				}
				gets.Add(1)
				wantToken := "imds-token"
				if strings.HasPrefix(mode, "timeout") {
					wantToken = ""
				}
				if r.Header.Get(credentials.TokenRequestHeader) != wantToken {
					t.Error("invalid instance metadata authorization")
				}
				if strings.HasSuffix(r.URL.Path, "/") {
					_, _ = io.WriteString(w, "first-role\nsecond-role\n")
				} else if r.URL.Path == credentials.DefaultIAMSecurityCredsPath+"first-role" {
					writeRoleCredentials(w, "instance-access")
				} else {
					t.Error("requested the wrong instance role")
				}
			}))
			defer server.Close()
			t.Setenv("MC_STS_ENDPOINT_cacheA", server.URL)
			p := credentialTestProvider(t, "cacheA")
			t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/later-identity")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer deadlineCancel()
			}
			if mode == "cancel" {
				go func() {
					<-requested
					cancel()
				}()
			}
			value, err := p.RetrieveWithCredContext(&credentials.CredContext{Context: ctx})
			switch mode {
			case "cancel", "deadline":
				if !errors.Is(err, ctx.Err()) || err == nil || gets.Load() != 0 {
					t.Error("caller cancellation fell back to IMDSv1")
				}
			case "refused":
				if err == nil || gets.Load() != 0 {
					t.Error("non-timeout token failure fell back to IMDSv1")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if value.AccessKeyID != "instance-access" || gets.Load() != 2 {
					t.Error("instance metadata retrieval failed")
				}
			}
			if puts.Load() != 1 {
				t.Fatalf("expected one token request, got %d", puts.Load())
			}
		})
	}
}

func TestClientCacheCredentialEnvironment(t *testing.T) {
	for _, mode := range []string{"web", "container", "token-file", "imds"} {
		t.Run(mode, func(t *testing.T) {
			isolateCacheTestEnv(t)
			t.Setenv("MC_STS_ENDPOINT_cacheA", "http://sts.original.test")
			changes := map[string]string{"MC_STS_ENDPOINT_cacheA": "http://sts.changed.test"}
			switch mode {
			case "web":
				t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/original-token")
				changes["AWS_WEB_IDENTITY_TOKEN_FILE"] = "/changed-token"
				changes["AWS_ROLE_ARN"] = "changed-role"
				changes["AWS_ROLE_SESSION_NAME"] = "changed-session"
			case "container":
				t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/relative")
				changes["AWS_CONTAINER_AUTHORIZATION_TOKEN"] = "changed-token"
			case "token-file":
				t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://container.original.test")
				t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "/original-token")
				changes["AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"] = "/changed-token"
				changes["AWS_CONTAINER_CREDENTIALS_FULL_URI"] = "http://container.changed.test"
				// This mode explicitly overrides MC_STS_ENDPOINT.
				delete(changes, "MC_STS_ENDPOINT_cacheA")
			case "imds":
				changes["AWS_WEB_IDENTITY_TOKEN_FILE"] = "/changed-token"
			}
			for name, admin := range map[string]bool{"S3": false, "Admin": true} {
				t.Run(name, func(t *testing.T) {
					for key, value := range changes {
						t.Run(key, func(t *testing.T) {
							factory := cacheTestFactory(admin)
							config := cacheTestConfig()
							config.Alias = "cacheA"
							first, err := factory(&config)
							if err != nil {
								t.Fatal(err)
							}
							same, err := factory(&config)
							if err != nil || same.api != first.api {
								t.Fatal("identical provider settings did not reuse the client")
							}
							t.Setenv(key, value)
							second, err := factory(&config)
							if err != nil {
								t.Fatal(err)
							}
							if first.api == second.api {
								t.Error("changed provider settings reused a cached client")
							}
						})
					}
				})
			}
		})
	}
}

func TestConfiguredIAMFallbackAndRedaction(t *testing.T) {
	isolateCacheTestEnv(t)
	const secret = "CACHE-TEST-SENSITIVE-MARKER"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, secret)
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), secret)
	writeCredentialToken(t, tokenPath, secret)
	t.Setenv("MC_STS_ENDPOINT_cacheA", server.URL+"?token="+secret)
	t.Setenv("MC_WEB_IDENTITY_TOKEN_FILE_cacheA", tokenPath)
	config := cacheTestConfig()
	config.Alias, config.Debug = "cacheA", true
	settings, err := snapshotClient(&config, "cache.identity.test:9000", isHostTLS(&config))
	if err != nil {
		t.Fatal(err)
	}
	provider := credentialTestProvider(t, config.Alias)
	if _, err := provider.RetrieveWithCredContext(&credentials.CredContext{Context: t.Context()}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("provider failure exposed sensitive data or was not reported")
	}
	_, creds := settings.transportAndCredentials()
	value, e := creds.GetWithContext(&credentials.CredContext{Context: t.Context()})
	if e != nil || value.AccessKeyID != config.AccessKey || value.SecretAccessKey != config.SecretKey {
		t.Fatal("unavailable STS provider did not fall back to configured static credentials")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		text := fmt.Sprintf(format, settings.key) + fmt.Sprintf(format, settings.key.provider)
		for _, sensitive := range []string{secret, config.AccessKey, config.SecretKey} {
			if strings.Contains(text, sensitive) {
				t.Error("formatting a cache key exposed sensitive data")
			}
		}
	}
	t.Setenv("MC_STS_ENDPOINT_cacheA", "http://"+secret+"%invalid")
	for name, admin := range map[string]bool{"S3": false, "Admin": true} {
		t.Run(name, func(t *testing.T) {
			_, err := cacheTestFactory(admin)(&config)
			if err == nil || strings.Contains(err.String(), secret) {
				t.Error("invalid credential endpoint exposed sensitive data or was accepted")
			}
		})
	}
}

func TestConfiguredIAMDebugRedaction(t *testing.T) {
	const secret = "CACHE-DEBUG-SENSITIVE-MARKER"
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// The generic trace redactor cannot recover tokens from the POST body.
		_, _ = io.WriteString(w, secret)
	}))
	defer sts.Close()
	tokenPath := filepath.Join(t.TempDir(), "web-token")
	writeCredentialToken(t, tokenPath, secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=access/") {
			t.Error("CLI did not use its static fallback")
		}
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer server.Close()
	result := runChecksumVerifyCLI(t, server.URL, []string{
		"MC_STS_ENDPOINT_b4=" + sts.URL, "MC_WEB_IDENTITY_TOKEN_FILE_b4=" + tokenPath,
	}, "--debug", "--json", "admin", "info", "b4")
	if result.exitCode != 0 {
		t.Fatalf("CLI failed with exit code %d", result.exitCode)
	}
	if strings.Contains(string(result.stdout)+string(result.stderr), secret) {
		t.Error("credential endpoint leaked a POST token into debug output")
	}
}
