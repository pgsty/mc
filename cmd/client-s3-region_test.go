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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/mc/pkg/probe"
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
// surface the tests need: HEAD on a bucket and the GetBucketLocation
// lookup. Every request is recorded so the tests can assert which region
// each one was signed with. The returned function reports the requests
// seen since the previous call.
func newRegionTestServer(t *testing.T) (*httptest.Server, func() []capturedRegionRequest) {
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
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
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

// newRegionTestConfig loads an alias configuration from the JSON a user
// hand-edits into config.json (the scenario of issue #5268), so the test
// exercises the same parse path as the real config file.
func newRegionTestConfig(t *testing.T, serverURL string, aliases map[string]string) *configV10 {
	t.Helper()

	entries := make([]string, 0, len(aliases))
	for alias, region := range aliases {
		entry := fmt.Sprintf(`%q:{"url":%q,"accessKey":"access","secretKey":"secret","api":"S3v4","path":"auto"`, alias, serverURL)
		if region != "" {
			entry += fmt.Sprintf(`,"region":%q`, region)
		}
		entry += "}"
		entries = append(entries, entry)
	}

	var cfg configV10
	raw := `{"version":"10","aliases":{` + strings.Join(entries, ",") + `}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("parsing test config %q: %v", raw, err)
	}
	return &cfg
}

// withRegionTestAliases swaps in the given configuration and clears the
// region environment overrides for the duration of the test.
func withRegionTestAliases(t *testing.T, cfg *configV10, mcRegion, awsRegion string) {
	t.Helper()

	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	// env.Get treats empty values as unset, so an empty assignment here
	// shields the test from MC_REGION/AWS_REGION leaking in from the shell.
	t.Setenv("MC_REGION", mcRegion)
	t.Setenv("AWS_REGION", awsRegion)

	oldLoad := loadMcConfig
	loadMcConfig = func() (*configV10, *probe.Error) { return cfg, nil }
	t.Cleanup(func() { loadMcConfig = oldLoad })
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
			server, requests := newRegionTestServer(t)
			cfg := newRegionTestConfig(t, server.URL, map[string]string{"rt": tc.aliasRegion})
			withRegionTestAliases(t, cfg, tc.mcRegion, tc.awsRegion)

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

// TestS3ClientCacheKeepsRegionIdentity verifies that the client cache
// does not hand out a client signed for one region when the alias asks
// for another: two aliases differing only in their region must each sign
// with their own region even though they share host and credentials.
func TestS3ClientCacheKeepsRegionIdentity(t *testing.T) {
	server, requests := newRegionTestServer(t)
	cfg := newRegionTestConfig(t, server.URL, map[string]string{
		"rt":  "us-west-1",
		"rt2": "eu-west-1",
	})
	withRegionTestAliases(t, cfg, "", "")

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

// TestGetConfigHashIncludesRegion pins the client cache identity: the
// hash must distinguish clients by the region they will sign with, and
// it must use the resolved region so an environment override collapses
// otherwise different alias regions into one cache entry.
func TestGetConfigHashIncludesRegion(t *testing.T) {
	base := func(region string) *Config {
		return &Config{
			HostURL:   "https://s3.local:9000",
			AccessKey: "access",
			SecretKey: "secret",
			Region:    region,
		}
	}

	t.Setenv("MC_REGION", "")
	t.Setenv("AWS_REGION", "")

	if getConfigHash(base("")) == getConfigHash(base("us-west-1")) {
		t.Error("cache hash must distinguish a client with an alias region from one without")
	}
	if getConfigHash(base("us-west-1")) == getConfigHash(base("eu-west-1")) {
		t.Error("cache hash must distinguish clients with different alias regions")
	}

	t.Setenv("MC_REGION", "eu-central-1")
	if getConfigHash(base("us-west-1")) != getConfigHash(base("eu-west-1")) {
		t.Error("with MC_REGION set, the resolved region is identical, so the cache hash must match too")
	}
}
