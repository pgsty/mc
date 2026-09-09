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
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGetPutFailureExitStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, status := "AccessDenied", http.StatusForbidden
		if r.URL.Path == "/bucket/missing" {
			code, status = "NoSuchKey", http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("X-Minio-Error-Code", code)
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
	}))
	defer server.Close()
	source := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"default", "quiet", "json"} {
		for _, tc := range []struct {
			name      string
			args      []string
			errorType string
		}{
			{"get local source", []string{"get", source, filepath.Join(t.TempDir(), "out")}, "error"},
			{"put missing source", []string{"put", source + "-missing", "b4/bucket/object"}, "error"},
			{"get missing object", []string{"get", "b4/bucket/missing", filepath.Join(t.TempDir(), "out")}, "fatal"},
			{"get denied transfer", []string{"get", "b4/bucket/denied", filepath.Join(t.TempDir(), "out")}, "fatal"},
			{"put denied transfer", []string{"put", source, "b4/bucket/denied"}, "fatal"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				args := append([]string{}, tc.args...)
				if mode != "default" {
					args = append([]string{"--" + mode}, args...)
				}
				result := runChecksumVerifyCLI(t, server.URL, []string{"MC_REGION=us-east-1"}, args...)
				if result.exitCode != 1 {
					t.Errorf("exit = %d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
				}
				combined := string(result.stdout) + string(result.stderr)
				if strings.Contains(combined, "Invalid command usage") {
					t.Errorf("transfer error reported as command usage: %s", combined)
				}
				if mode == "json" {
					decoder := json.NewDecoder(bytes.NewReader(result.stdout))
					errors, records := 0, 0
					var record struct {
						Status string `json:"status"`
						Total  *int64 `json:"total"`
						Error  struct {
							Type string `json:"type"`
						} `json:"error"`
					}
					for {
						if err := decoder.Decode(&record); err == io.EOF {
							break
						} else if err != nil {
							t.Fatalf("invalid JSON: %s (%v)", result.stdout, err)
						}
						records++
						if record.Total != nil {
							t.Errorf("failed transfer emitted success statistics: %s", result.stdout)
						}
						if record.Status == "error" {
							errors++
						}
					}
					// The existing transfer-start record may precede the final error.
					if errors != 1 || record.Status != "error" || record.Error.Type != tc.errorType || (tc.errorType == "error" && records != 1) {
						t.Errorf("wrong error contract: %s", result.stdout)
					}
					if len(result.stderr) != 0 {
						t.Errorf("JSON stderr = %s", result.stderr)
					}
				} else if len(result.stderr) == 0 {
					t.Error("missing error diagnostic")
				}
			})
		}
	}
}

func TestGetPutSuccessfulTransfers(t *testing.T) {
	for _, data := range [][]byte{[]byte("ordinary payload\n"), {}} {
		for _, mode := range []string{"default", "quiet", "json"} {
			t.Run(fmt.Sprintf("%s/%d bytes", mode, len(data)), func(t *testing.T) {
				var mu sync.Mutex
				var uploaded []byte
				var puts int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/bucket/object" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						http.NotFound(w, r)
						return
					}
					w.Header().Set("Last-Modified", time.Unix(100, 0).UTC().Format(http.TimeFormat))
					w.Header().Set("ETag", `"test-etag"`)
					switch r.Method {
					case http.MethodPut:
						var reader io.Reader = r.Body
						if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
							reader = httputil.NewChunkedReader(reader)
						}
						body, err := io.ReadAll(reader)
						if err != nil {
							t.Error(err)
						}
						mu.Lock()
						uploaded = body
						puts++
						mu.Unlock()
					case http.MethodGet, http.MethodHead:
						w.Header().Set("Content-Length", fmt.Sprint(len(data)))
						if r.Method == http.MethodGet {
							_, _ = w.Write(data)
						}
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusMethodNotAllowed)
					}
				}))
				defer server.Close()
				source, target := filepath.Join(t.TempDir(), "input"), filepath.Join(t.TempDir(), "output")
				if err := os.WriteFile(source, data, 0o600); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"put", source, "b4/bucket/object"}, {"get", "b4/bucket/object", target}} {
					if mode != "default" {
						args = append([]string{"--" + mode}, args...)
					}
					result := runChecksumVerifyCLI(t, server.URL, []string{"MC_REGION=us-east-1"}, args...)
					if result.exitCode != 0 {
						t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
					}
				}
				got, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(got, data) {
					t.Errorf("download = %q, err=%v, want %q", got, err, data)
				}
				mu.Lock()
				defer mu.Unlock()
				if puts != 1 || !bytes.Equal(uploaded, data) {
					t.Errorf("PUT count=%d body=%q, want %q", puts, uploaded, data)
				}
			})
		}
	}
}
