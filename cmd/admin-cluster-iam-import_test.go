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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zip"
)

func iamImportArchive(t *testing.T) (string, []byte) {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	member, err := archive.Create("iam-assets/users.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(member, `{}`); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "iam.zip")
	if err = os.WriteFile(filename, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename, data.Bytes()
}

func TestIAMImportCLI(t *testing.T) {
	archive, payload := iamImportArchive(t)
	cases := []struct {
		name, body, wantText string
		status, exit, legacy int
	}{
		{name: "success", body: `{"added":{"users":["good-user"]},"skipped":{"groups":["kept-group"]},"removed":{"policies":["empty-policy"]}}`, wantText: "good-user"},
		{name: "mixed", body: `{"added":{"users":["good-user"]},"failed":{"users":[{"name":"bad-user","error":{}}]}}`, wantText: "bad-user", exit: 1},
		{name: "bad json", body: `{"failed":`, wantText: "Unable to import IAM info", exit: 1},
		{name: "trailing json", body: `{} {}`, wantText: "Unable to import IAM info", exit: 1},
		{name: "forbidden", status: 403, body: `{"Code":"AccessDenied","Message":"restore denied"}`, wantText: "restore denied", exit: 1},
		{name: "forbidden XML", status: 403, body: `<Error><Code>AccessDenied</Code><Message>restore denied</Message></Error>`, wantText: "restore denied", exit: 1},
		{name: "unsupported XML", status: 501, body: `<Error><Code>NotImplemented</Code><Message>unsupported API</Message></Error>`, legacy: 1},
		{name: "error text", body: `{"failed":{"users":[{"name":"bad-user","error":"denied secret credential"}]}}`, wantText: "denied", exit: 1},
		{name: "error object secret", body: `{"failed":{"users":[{"name":"bad-user","error":{"Message":"denied secret credential"}}]}}`, wantText: "denied", exit: 1},
		{name: "not found", status: 404, body: `{"Code":"NotFound","Message":"route missing"}`, wantText: "route missing", exit: 1},
		{name: "unexpected successful status", status: 204, wantText: "HTTP 204", exit: 1},
		{name: "unsupported", status: 501, body: `{"Code":"NotImplemented","Message":"unsupported API"}`, legacy: 1},
	}
	for _, category := range []string{"users", "policies", "groups", "serviceAccounts", "userPolicies", "groupPolicies", "stsPolicies"} {
		cases = append(cases, struct {
			name, body, wantText string
			status, exit, legacy int
		}{
			name: category, body: fmt.Sprintf(`{"failed":{%q:[{"name":"bad-entity","policies":["readwrite"],"error":{"Message":"entity rejected"}}]}}`, category), wantText: "entity rejected", exit: 1,
		})
	}
	for _, mode := range []string{"text", "json"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var modern, legacy atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(body, payload) {
						t.Errorf("archive changed: read error=%v", err)
					}
					if r.Method != http.MethodPut || r.Header.Get("Authorization") == "" {
						t.Errorf("expected signed PUT, got %s", r.Method)
					}
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/minio/admin/v3/import-iam-v2":
						modern.Add(1)
						status := tc.status
						if status == 0 {
							status = http.StatusOK
						}
						w.WriteHeader(status)
						_, _ = io.WriteString(w, tc.body)
					case "/minio/admin/v3/import-iam":
						legacy.Add(1)
						w.WriteHeader(http.StatusOK)
					default:
						t.Errorf("unexpected request: %s", r.URL)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				args := []string{"admin", "cluster", "iam", "import", "b4", archive}
				if mode == "json" {
					args = append([]string{"--json"}, args...)
				}
				result := runChecksumVerifyCLI(t, server.URL, nil, args...)
				output := string(result.stdout) + string(result.stderr)
				if result.exitCode != tc.exit || modern.Load() != 1 || legacy.Load() != int32(tc.legacy) {
					t.Fatalf("exit=%d modern=%d legacy=%d; output=%s", result.exitCode, modern.Load(), legacy.Load(), output)
				}
				if strings.Contains(output, "secret credential") {
					t.Fatalf("server error leaked registered secret: %s", output)
				}
				if !strings.Contains(output, tc.wantText) {
					t.Fatalf("missing %q: %s", tc.wantText, output)
				}
				if tc.name == "mixed" && !strings.Contains(output, "good-user") {
					t.Fatalf("lost successful entity: %s", output)
				}
				if mode == "json" && tc.legacy == 0 {
					var report map[string]json.RawMessage
					if err := json.Unmarshal(result.stdout, &report); err != nil {
						t.Fatalf("invalid JSON: %v: %s", err, output)
					}
					if tc.name == "mixed" {
						var failed iamImportFailures
						if err := json.Unmarshal(report["failed"], &failed); err != nil {
							t.Fatal(err)
						}
						if len(failed.Users) != 1 || string(failed.Users[0].Error) != "{}" {
							t.Fatalf("lost failure detail: %s", output)
						}
					}
				}
			})
		}
	}
}

func TestIAMImportTransportFailureCLI(t *testing.T) {
	archive, _ := iamImportArchive(t)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	result := runChecksumVerifyCLI(t, endpoint, nil, "--json", "admin", "cluster", "iam", "import", "b4", archive)
	if result.exitCode != 1 || !bytes.Contains(result.stdout, []byte("Unable to import IAM info")) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func TestIAMImportReportScrubsSecrets(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	const secret = "iam-secret<&>0123456789"
	registerSecret(secret)
	raw, err := json.Marshal(map[string]any{"Message": "rejected " + secret, "code": 42, "details": []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	report := iamImportInfo{Failed: iamImportFailures{Users: []iamImportFailure{{Name: "user", Error: raw}}}}
	for _, output := range []string{report.String(), report.JSON()} {
		if strings.Contains(output, "0123456789") || !strings.Contains(output, redactedMarker) {
			t.Fatalf("unsafe error output: %s", output)
		}
	}
	var decoded iamImportInfo
	if err := json.Unmarshal([]byte(report.JSON()), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Failed.Users) != 1 {
		t.Fatal("lost failed entity")
	}
	var detail map[string]any
	if err := json.Unmarshal(decoded.Failed.Users[0].Error, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["code"] != float64(42) {
		t.Fatalf("changed JSON structure: %v", detail)
	}
}
