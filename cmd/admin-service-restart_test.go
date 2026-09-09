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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/minio/madmin-go/v3"
)

func TestServiceRestartDryRunNeverFallsBack(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry-run=%t", dryRun), func(t *testing.T) {
			var modern, legacy atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/minio/admin/v3/service" || r.URL.Query().Get("action") != "restart" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("type") == "2" {
					modern.Add(1)
					if r.URL.Query().Get("dry-run") != fmt.Sprint(dryRun) {
						t.Errorf("wrong dry-run: %s", r.URL)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"Code":"AccessDenied","Message":"fixture rejects service action"}`)
				} else {
					legacy.Add(1)
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()
			args := []string{"--json", "admin", "service", "restart"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			result := runChecksumVerifyCLI(t, server.URL, nil, append(args, "b4")...)
			wantExit, wantLegacy := 0, int32(1)
			if dryRun {
				wantExit, wantLegacy = 1, 0
			}
			if result.exitCode != wantExit || modern.Load() != 1 || legacy.Load() != wantLegacy {
				t.Fatalf("exit=%d modern=%d legacy=%d, want exit=%d legacy=%d; stdout=%s stderr=%s", result.exitCode, modern.Load(), legacy.Load(), wantExit, wantLegacy, result.stdout, result.stderr)
			}
		})
	}
}

func newServiceRestartTestServer(t *testing.T, unhealthy bool) *httptest.Server {
	t.Helper()
	var healthCalls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/minio/admin/v3/service":
			if r.URL.Query().Get("type") != "2" || r.URL.Query().Get("action") != "restart" {
				t.Errorf("unexpected restart request: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(madmin.ServiceActionResult{
				Action: madmin.ServiceActionRestart, DryRun: r.URL.Query().Get("dry-run") == "true",
				Results: []madmin.ServiceActionPeerResult{
					{Host: "online"}, {Host: "offline", Err: "unreachable"}, {Host: "hung", WaitingDrives: map[string]madmin.DiskMetrics{"drive": {}}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/minio/health/cluster":
			// Exercise an unhealthy poll before the terminal healthy response.
			if healthCalls.Add(1) == 1 && unhealthy {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
}

func TestServiceRestartJSONCompletion(t *testing.T) {
	for _, wait := range []bool{false, true} {
		for attempt := 0; attempt < 10; attempt++ {
			t.Run(fmt.Sprintf("wait=%t/%d", wait, attempt), func(t *testing.T) {
				server := newServiceRestartTestServer(t, wait)
				defer server.Close()
				args := []string{"--json", "admin", "service", "restart", "--dry-run"}
				if wait {
					args = append(args, "--wait")
				}
				result := runChecksumVerifyCLI(t, server.URL, nil, append(args, "b4")...)
				if result.exitCode != 0 || len(result.stderr) != 0 {
					t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
				}
				decoder := json.NewDecoder(bytes.NewReader(result.stdout))
				var states []int
				doneCount := 0
				for {
					var msg serviceRestartMessage
					if err := decoder.Decode(&msg); err == io.EOF {
						break
					} else if err != nil {
						t.Fatalf("invalid JSON: %v: %s", err, result.stdout)
					}
					if msg.Status != "success" || !msg.Result.DryRun || msg.ServerURL != "b4" || len(msg.Result.Results) != 3 {
						t.Errorf("changed JSON structure: %s", result.stdout)
					}
					states = append(states, msg.State)
					if msg.State == done {
						doneCount++
					}
				}
				if doneCount != 1 || len(states) == 0 || states[len(states)-1] != done {
					t.Fatalf("missing or duplicate final done: states=%v stdout=%s", states, result.stdout)
				}
				if wait && (len(states) < 3 || states[0] != restarting || states[1] != waiting) {
					t.Errorf("missing wait progress: %v", states)
				}
				if !wait && len(states) != 1 {
					t.Errorf("unexpected non-wait progress: %v", states)
				}
			})
		}
	}
}
