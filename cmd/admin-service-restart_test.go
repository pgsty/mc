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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
