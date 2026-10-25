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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminInfoExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		json     bool
		denied   bool
		wantExit int
	}{
		{name: "json denied", json: true, denied: true, wantExit: 1},
		{name: "text denied", denied: true, wantExit: 1},
		{name: "json success", json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/minio/admin/v3/info" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.denied {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"Code":"AccessDenied","Message":"Access Denied."}`)
					return
				}
				_, _ = io.WriteString(w, `{"servers":[]}`)
			}))
			defer server.Close()

			args := []string{"admin", "info", "b4"}
			if tc.json {
				args = append([]string{"--json"}, args...)
			}
			result := runChecksumVerifyCLI(t, server.URL, nil, args...)
			if result.exitCode != tc.wantExit {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", result.exitCode, tc.wantExit, result.stdout, result.stderr)
			}
			if tc.json {
				var record clusterStruct
				if err := json.Unmarshal(result.stdout, &record); err != nil {
					t.Fatalf("expected one JSON record: %v; stdout=%s", err, result.stdout)
				}
				wantStatus := "success"
				if tc.denied {
					wantStatus = "error"
					if record.Error != "Access Denied." {
						t.Fatalf("unexpected error: %q", record.Error)
					}
				}
				if record.Status != wantStatus || len(result.stderr) != 0 {
					t.Fatalf("status=%q stderr=%s", record.Status, result.stderr)
				}
			} else if !bytes.Contains(result.stderr, []byte("Access Denied")) {
				t.Fatalf("missing text error: %s", result.stderr)
			}
		})
	}
}
