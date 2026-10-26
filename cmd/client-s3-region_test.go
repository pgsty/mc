// Copyright (c) 2026 PGSTY
//
// This file is part of the Silo object storage client.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedRegionRequest records one request seen by the region test
// server, together with the region of the AWS SigV4 credential scope it
// was signed with.
type capturedRegionRequest struct {
	method string
	path   string
	query  string
	scope  string // region component of the SigV4 Credential scope, "" when unsigned
}

// sigv4ScopeRegion extracts the region from an Authorization header of
// the form `AWS4-HMAC-SHA256 Credential=AK/20260918/us-west-1/s3/aws4_request, ...`.
func sigv4ScopeRegion(authorization string) string {
	const prefix = "AWS4-HMAC-SHA256 Credential="
	for _, part := range strings.Split(authorization, ",") {
		part = strings.TrimSpace(part)
		if rest, ok := strings.CutPrefix(part, prefix); ok {
			fields := strings.Split(rest, "/")
			if len(fields) >= 3 {
				return fields[2]
			}
		}
	}
	return ""
}

// newRegionTestServer starts an httptest server answering the minimal S3
// surface the tests need: HEAD on a bucket, PUT on an object, and the
// GetBucketLocation lookup. Every request is recorded to assert which region
// each one was signed with. The returned function reports the requests
// seen since the previous call.
func newRegionTestServer(t *testing.T, rejectLocation bool) (*httptest.Server, func() []capturedRegionRequest) {
	t.Helper()

	var mutex sync.Mutex
	var seen []capturedRegionRequest
	var consumed int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// minio-go addresses a bucket as /bucket/, normalize for matching.
		path := strings.TrimSuffix(r.URL.Path, "/")
		mutex.Lock()
		seen = append(seen, capturedRegionRequest{
			method: r.Method,
			path:   path,
			query:  r.URL.RawQuery,
			scope:  sigv4ScopeRegion(r.Header.Get("Authorization")),
		})
		mutex.Unlock()

		switch {
		case r.Method == http.MethodHead && path == "/bucket":
			w.Header().Set("Last-Modified", time.Unix(100, 0).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && path == "/bucket" && r.URL.Query().Has("location"):
			if rejectLocation {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotImplemented)
				w.Write([]byte(`<Error><Code>NotImplemented</Code><Message>GetBucketLocation not implemented</Message></Error>`))
				return
			}
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
		case r.Method == http.MethodPut && path == "/bucket/object":
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Errorf("reading upload: %v", err)
			}
			w.Header().Set("ETag", `"5d41402abc4b2a76b9719d911017c592"`)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	return server, func() []capturedRegionRequest {
		mutex.Lock()
		defer mutex.Unlock()
		fresh := append([]capturedRegionRequest(nil), seen[consumed:]...)
		consumed = len(seen)
		return fresh
	}
}

// withRegionTestConfig uses a temporary config.json and the production
// loader, restoring the config and client caches when the test finishes.
func withRegionTestConfig(t *testing.T, raw string) {
	t.Helper()

	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	// env.Get treats empty values as unset, so an empty assignment here
	// shields the test from MC_REGION/AWS_REGION leaking in from the shell.
	t.Setenv("MC_REGION", "")
	t.Setenv("AWS_REGION", "")

	oldDir, oldCache, oldLoad, oldFactory := mcCustomConfigDir, cacheCfgV10, loadMcConfig, S3New
	setMcConfigDir(t.TempDir())
	cacheCfgV10 = nil
	S3New = newFactory()
	t.Cleanup(func() {
		mcCustomConfigDir, cacheCfgV10, loadMcConfig, S3New = oldDir, oldCache, oldLoad, oldFactory
	})
	if err := os.WriteFile(mustGetMcConfigPath(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	loadMcConfig = loadMcConfigFactory()
	if _, err := loadMcConfig(); err != nil {
		t.Fatal(err)
	}
}

func withRegionTestAliases(t *testing.T, serverURL string, aliases map[string]string, mcRegion, awsRegion string) {
	t.Helper()

	entries := make([]string, 0, len(aliases))
	for alias, region := range aliases {
		entry := fmt.Sprintf(`%q:{"url":%q,"accessKey":"access","secretKey":"secret","api":"S3v4","path":"auto"`, alias, serverURL)
		if region != "" {
			entry += fmt.Sprintf(`,"region":%q`, region)
		}
		entries = append(entries, entry+"}")
	}
	withRegionTestConfig(t, `{"version":"10","aliases":{`+strings.Join(entries, ",")+`}}`)
	t.Setenv("MC_REGION", mcRegion)
	t.Setenv("AWS_REGION", awsRegion)
}

// statTestBucket creates a client for alias/bucket and stats the bucket,
// returning every request the server saw.
func statTestBucket(t *testing.T, alias string, requests func() []capturedRegionRequest) []capturedRegionRequest {
	t.Helper()

	client, err := newClient(alias + "/bucket")
	if err != nil {
		t.Fatalf("creating client for %s: %v", alias, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = client.Stat(ctx, StatOptions{}); err != nil {
		t.Fatalf("stat %s/bucket: %v", alias, err)
	}
	seen := requests()
	if len(seen) == 0 {
		t.Fatal("no request reached the server")
	}
	return seen
}

// TestS3ClientRegionPrecedence pins the region the S3 client signs with:
// the MC_REGION and AWS_REGION environment overrides win over the region
// configured on the alias, and when nothing is configured the bucket
// location lookup discovers it automatically.
func TestS3ClientRegionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name            string
		aliasRegion     string
		mcRegion        string
		awsRegion       string
		wantRegion      string // expected SigV4 credential scope of every request
		wantLocationGet bool   // whether automatic bucket-location discovery is expected
	}{
		{name: "alias region is honored", aliasRegion: "us-west-1", wantRegion: "us-west-1"},
		{name: "MC_REGION overrides alias region", aliasRegion: "us-west-1", mcRegion: "eu-central-1", wantRegion: "eu-central-1"},
		{name: "AWS_REGION overrides alias region", aliasRegion: "us-west-1", awsRegion: "eu-north-1", wantRegion: "eu-north-1"},
		{name: "MC_REGION overrides AWS_REGION", aliasRegion: "us-west-1", mcRegion: "eu-central-1", awsRegion: "eu-north-1", wantRegion: "eu-central-1"},
		{name: "no explicit region discovers automatically", wantRegion: "us-east-1", wantLocationGet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newRegionTestServer(t, false)
			withRegionTestAliases(t, server.URL, map[string]string{"rt": tc.aliasRegion}, tc.mcRegion, tc.awsRegion)

			seen := statTestBucket(t, "rt", requests)

			var locationSeen bool
			for _, req := range seen {
				if req.scope != tc.wantRegion {
					t.Errorf("request %s %s?%s signed with region %q, want %q",
						req.method, req.path, req.query, req.scope, tc.wantRegion)
				}
				if req.method == http.MethodGet && req.path == "/bucket" && req.query == "location=" {
					locationSeen = true
				}
			}
			if locationSeen != tc.wantLocationGet {
				t.Errorf("bucket location lookup = %v, want %v", locationSeen, tc.wantLocationGet)
			}
		})
	}
}

// A configured alias must work even when the endpoint does not implement
// GetBucketLocation. Without a region, discovery still fails as before.
func TestS3ClientRegionWithoutLocationAPI(t *testing.T) {
	for _, operation := range []string{"stat", "put"} {
		for _, region := range []string{"", "us-west-1"} {
			t.Run(operation+"/region="+region, func(t *testing.T) {
				server, requests := newRegionTestServer(t, true)
				withRegionTestAliases(t, server.URL, map[string]string{"rt": region}, "", "")

				path := "rt/bucket"
				if operation == "put" {
					path += "/object"
				}
				client, err := newClient(path)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if operation == "put" {
					_, err = client.Put(ctx, strings.NewReader("hello"), 5, nil, PutOptions{disableMultipart: true})
				} else {
					_, err = client.Stat(ctx, StatOptions{})
				}
				if (err != nil) != (region == "") {
					t.Fatalf("operation error = %v, want error = %v", err, region == "")
				}
				seen := requests()
				if len(seen) != 1 {
					t.Fatalf("expected one request, got %+v", seen)
				}
				if region == "" {
					if seen[0].method != http.MethodGet || seen[0].query != "location=" {
						t.Errorf("expected failed location lookup, got %+v", seen[0])
					}
					return
				}
				wantMethod := http.MethodHead
				if operation == "put" {
					wantMethod = http.MethodPut
				}
				if seen[0].method != wantMethod || seen[0].query != "" || seen[0].scope != region {
					t.Errorf("expected %s signed with %s without location lookup, got %+v", wantMethod, region, seen[0])
				}
			})
		}
	}
}

func TestConfigRegionRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		field      string
		wantRegion string
	}{
		{name: "missing"},
		{name: "empty", field: `,"region":""`},
		{name: "null", field: `,"region":null`},
		{name: "configured", field: `,"region":"us-west-1"`, wantRegion: "us-west-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := newRegionTestServer(t, false)
			raw := fmt.Sprintf(`{"version":"10","aliases":{"rt":{"url":%q,"accessKey":"access","secretKey":"secret","api":"S3v4","path":"auto"%s}}}`, server.URL, tc.field)
			withRegionTestConfig(t, raw)
			cfg, err := loadMcConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Aliases["rt"].Region != tc.wantRegion {
				t.Fatalf("loaded region = %q, want %q", cfg.Aliases["rt"].Region, tc.wantRegion)
			}
			if err := saveMcConfig(cfg); err != nil {
				t.Fatal(err)
			}
			saved, readErr := os.ReadFile(mustGetMcConfigPath())
			if readErr != nil {
				t.Fatal(readErr)
			}
			var decoded struct {
				Aliases map[string]map[string]json.RawMessage `json:"aliases"`
			}
			if err := json.Unmarshal(saved, &decoded); err != nil {
				t.Fatal(err)
			}
			_, hasRegion := decoded.Aliases["rt"]["region"]
			if hasRegion != (tc.wantRegion != "") {
				t.Errorf("saved region field present = %v, want %v", hasRegion, tc.wantRegion != "")
			}

			// Force the next client to read the saved file instead of either cache.
			cacheCfgV10 = nil
			loadMcConfig = loadMcConfigFactory()
			wantRegion := tc.wantRegion
			if wantRegion == "" {
				wantRegion = "us-east-1"
			}
			var locationSeen bool
			for _, req := range statTestBucket(t, "rt", requests) {
				if req.scope != wantRegion {
					t.Errorf("request signed with %q after save/reload, want %q", req.scope, wantRegion)
				}
				locationSeen = locationSeen || req.query == "location="
			}
			if locationSeen != (tc.wantRegion == "") {
				t.Errorf("location lookup after save/reload = %v, want %v", locationSeen, tc.wantRegion == "")
			}
		})
	}
}

// TestS3ClientCacheKeepsRegionIdentity verifies that the client cache
// does not hand out a client signed for one region when the alias asks
// for another: two aliases differing only in their region must each sign
// with their own region even though they share host and credentials.
func TestS3ClientCacheKeepsRegionIdentity(t *testing.T) {
	server, requests := newRegionTestServer(t, false)
	withRegionTestAliases(t, server.URL, map[string]string{
		"rt":  "us-west-1",
		"rt2": "eu-west-1",
	}, "", "")

	for alias, wantRegion := range map[string]string{
		"rt":  "us-west-1",
		"rt2": "eu-west-1",
	} {
		for _, req := range statTestBucket(t, alias, requests) {
			if req.scope != wantRegion {
				t.Errorf("%s: request %s %s?%s signed with region %q, want %q",
					alias, req.method, req.path, req.query, req.scope, wantRegion)
			}
		}
	}
}

// TestClientCacheKeyIncludesRegion pins the client cache identity: the
// key must distinguish clients by the region they will sign with, and
// it must use the resolved region so an environment override collapses
// otherwise different alias regions into one cache entry.
func TestClientCacheKeyIncludesRegion(t *testing.T) {
	key := func(region string) clientCacheKey {
		config := &Config{
			HostURL:   "https://s3.local:9000",
			AccessKey: "access",
			SecretKey: "secret",
			Region:    region,
		}
		settings, err := snapshotClient(config, "s3.local:9000", isHostTLS(config))
		if err != nil {
			t.Fatal(err)
		}
		return settings.key
	}

	t.Setenv("MC_REGION", "")
	t.Setenv("AWS_REGION", "")

	if key("") == key("us-west-1") {
		t.Error("cache key must distinguish a client with an alias region from one without")
	}
	if key("us-west-1") == key("eu-west-1") {
		t.Error("cache key must distinguish clients with different alias regions")
	}

	t.Setenv("MC_REGION", "eu-central-1")
	if key("us-west-1") != key("eu-west-1") {
		t.Error("with MC_REGION set, the resolved region is identical, so the cache key must match too")
	}
}
