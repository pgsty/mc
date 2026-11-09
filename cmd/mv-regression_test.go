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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/minio/mc/pkg/probe"
)

func TestMoveRejectsNormalizedSelfPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "object")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil,
		"--json", "mv", source, dir+string(filepath.Separator)+"."+string(filepath.Separator)+"object")
	if result.exitCode == 0 {
		t.Fatalf("self move succeeded: stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "keep" {
		t.Fatalf("self move changed source: data=%q error=%v", got, err)
	}
}

func TestMoveRejectsExpandedSelfDestinations(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
			for _, file := range []string{a, b} {
				if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--json", "mv", a, b, dir + string(filepath.Separator)}
			if kind != "directory" {
				link := filepath.Join(dir, "link")
				linkFn := os.Symlink
				if kind == "hardlink" {
					linkFn = os.Link
				}
				if err := linkFn(a, link); err != nil {
					t.Skipf("link unavailable: %v", err)
				}
				args = []string{"--json", "mv", a, link}
			}
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
			if result.exitCode != 1 || !strings.Contains(string(result.stdout), "itself") {
				t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			for _, file := range []string{a, b} {
				if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
					t.Fatalf("self move changed %s: data=%q error=%v", file, data, err)
				}
			}
		})
	}
}

func TestSameCopyObjectNormalizesResolvedPaths(t *testing.T) {
	urls := URLs{
		SourceContent: &ClientContent{URL: ClientURL{Type: fileSystem, Path: "/tmp/move/object"}},
		TargetContent: &ClientContent{URL: ClientURL{Type: fileSystem, Path: "/tmp/move/./object"}},
	}
	if !sameCopyObject(urls) {
		t.Fatal("equivalent source and destination paths were not detected")
	}
}

func TestMoveRejectsEquivalentEndpointNames(t *testing.T) {
	for _, customResolve := range []bool{false, true} {
		t.Run(fmt.Sprintf("resolve=%v", customResolve), func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("ETag", `"etag"`)
				w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
				switch {
				case r.URL.Query().Has("location"):
					_, _ = io.WriteString(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
				case r.URL.Query().Has("object-lock"):
					_, _ = io.WriteString(w, `<ObjectLockConfiguration/>`)
				case r.Method == http.MethodHead || r.Method == http.MethodGet:
					w.Header().Set("Content-Length", "4")
					if r.Method == http.MethodGet {
						_, _ = io.WriteString(w, "keep")
					}
				case r.Method == http.MethodPut:
					mutations.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
				case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
					mutations.Add(1)
					_, _ = io.WriteString(w, `<DeleteResult><Deleted><Key>object</Key></Deleted></DeleteResult>`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
			args := []string{"--json"}
			if customResolve {
				endpoint = strings.Replace(server.URL, "127.0.0.1", "same-store.test", 1)
				args = append(args, "--resolve", strings.TrimPrefix(endpoint, "http://")+"=127.0.0.1")
			}
			env := []string{
				"MC_REGION=us-east-1",
				"MC_HOST_peer=" + strings.Replace(endpoint, "://", "://access:secret@", 1),
				"NO_PROXY=127.0.0.1,localhost,same-store.test",
			}
			args = append(args, "mv", "b4/bucket/object", "peer/bucket/object")
			result := runChecksumVerifyCLI(t, server.URL, env, args...)
			if result.exitCode != 1 || mutations.Load() != 0 || !strings.Contains(string(result.stdout), "share a network address") {
				t.Fatalf("exit=%d mutations=%d stdout=%s stderr=%s", result.exitCode, mutations.Load(), result.stdout, result.stderr)
			}
		})
	}
}

func TestMoveEndpointGuardBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, source, target string
		blocked              bool
	}{
		{"shared virtual host", "http://source.test:9000/bucket/key", "http://target.test:9000/bucket/key", true},
		{"distinct server", "http://source.test:9000/bucket/key", "http://other.test:9000/bucket/key", false},
		{"distinct port", "http://source.test:9000/bucket/key", "http://target.test:9001/bucket/key", false},
		{"distinct scheme", "http://source.test:9000/bucket/key", "https://target.test:9000/bucket/key", false},
		{"distinct bucket", "http://source.test:9000/bucket/key", "http://target.test:9000/other/key", false},
		{"literal key", "http://source.test:9000/bucket/a/../key", "http://target.test:9000/bucket/key", false},
		{"default port", "http://127.0.0.1/bucket/key", "http://127.0.0.1:80/bucket/key", true},
		{"mapped IPv4", "http://127.0.0.1:9000/bucket/key", "http://[::ffff:127.0.0.1]:9000/bucket/key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			urls := URLs{
				SourceContent: &ClientContent{URL: *newClientURL(tc.source)},
				TargetContent: &ClientContent{URL: *newClientURL(tc.target)},
			}
			resolved := map[string]moveEndpointResolution{
				"source.test:9000": {ips: []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.3")}},
				"target.test:9000": {ips: []netip.Addr{netip.MustParseAddr("192.0.2.4"), netip.MustParseAddr("192.0.2.1")}},
				"other.test:9000":  {ips: []netip.Addr{netip.MustParseAddr("192.0.2.2")}},
			}
			// Cached addresses remain sufficient even when no further DNS
			// lookup can run, keeping the guard independent of batch size.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := checkMoveTarget(ctx, urls, resolved)
			if tc.blocked {
				if err == nil || !strings.Contains(err.Cause.Error(), "share a network address") {
					t.Fatalf("expected ambiguous endpoint rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("independent move rejected: %v", err)
			}
		})
	}
}

// clearProxyEnv makes endpoint resolution tests independent of the caller's proxy.
func clearProxyEnv(t *testing.T) {
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "no_proxy", "NO_PROXY", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}
}

func TestMoveEndpointLookupFailure(t *testing.T) {
	clearProxyEnv(t)
	urls := URLs{
		SourceContent: &ClientContent{URL: *newClientURL("http://unresolved.invalid:9000/bucket/key")},
		TargetContent: &ClientContent{URL: *newClientURL("http://127.0.0.1:9000/bucket/key")},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resolved := make(map[string]moveEndpointResolution)
	first := checkMoveTarget(ctx, urls, resolved)
	if first == nil {
		t.Fatal("unresolved endpoint allowed a destructive move")
	}
	if next := checkMoveTarget(t.Context(), urls, resolved); next == nil || next.Cause != first.Cause {
		t.Fatal("failed endpoint resolution was not retained for the session")
	}
}

func TestMoveProxiedEndpointLookupFailure(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "direct-only.invalid")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	check := func(source, target string) *probe.Error {
		return checkMoveTarget(ctx, URLs{
			SourceContent: &ClientContent{URL: *newClientURL(source)},
			TargetContent: &ClientContent{URL: *newClientURL(target)},
		}, make(map[string]moveEndpointResolution))
	}
	// The proxy, not local DNS, resolves this endpoint: an unresolved local
	// lookup must not block moving to a distinct store.
	if err := check("http://127.0.0.1:9000/bucket/key", "http://proxied-only.invalid:9000/bucket/key"); err != nil {
		t.Fatalf("proxied endpoint unresolved by local DNS was refused: %v", err)
	}
	// NO_PROXY makes the connection direct, so an unresolved name still fails closed.
	if check("http://127.0.0.1:9000/bucket/key", "http://direct-only.invalid:9000/bucket/key") == nil {
		t.Fatal("unresolved direct endpoint allowed a destructive move")
	}
	// The literal self-move check still applies through a proxy.
	if check("http://proxied-only.invalid:9000/bucket/key", "http://PROXIED-ONLY.invalid:9000/bucket/key") == nil {
		t.Fatal("proxied self-move was allowed")
	}
}
