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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zip"
	"github.com/minio/madmin-go/v3"
)

func TestBucketMetadataImportCLI(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	member, err := writer.Create("bucket/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(member, `{"Version":"2012-10-17","Statement":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "bucket-metadata.zip")
	if err := os.WriteFile(filename, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	type testCase struct {
		name, body, summary, detail string
		exit                        int
	}
	cases := []testCase{
		{name: "success", body: `{"buckets":{"first":{"policy":{"isSet":true}},"second":{"versioning":{"isSet":true}}}}`, summary: "2/2 buckets were imported successfully"},
		{name: "empty", body: `{"buckets":{}}`, summary: "0/0 buckets were imported successfully"},
		{name: "mixed", body: `{"buckets":{"good":{"policy":{"isSet":true}},"bad":{"policy":{"isSet":true,"error":"policy rejected"}}}}`, summary: "1/2 buckets were imported successfully", detail: "Policy:", exit: 1},
	}
	// The SDK has nine per-configuration statuses plus the bucket-level error.
	for _, field := range []struct{ key, label string }{
		{"olock", "Object lock:"},
		{"versioning", "Versioning:"},
		{"policy", "Policy:"},
		{"tagging", "Tagging:"},
		{"sse", "Encryption:"},
		{"lifecycle", "Lifecycle:"},
		{"notification", "Notification:"},
		{"quota", "Quota:"},
		{"cors", "CORS:"},
		{"error", "bucket rejected"},
	} {
		for _, isSet := range []bool{true, false} {
			if field.key == "error" && !isSet {
				continue
			}
			status := fmt.Sprintf(`{%q:{"isSet":%t,"error":"configuration rejected"}}`, field.key, isSet)
			detail := field.label
			if !isSet {
				detail = "bad-bucket"
			}
			if field.key == "error" {
				status = `{"error":"bucket rejected"}`
			}
			cases = append(cases, testCase{
				name: fmt.Sprintf("%s/set=%t", field.key, isSet),
				body: `{"buckets":{"bad-bucket":` + status + `}}`, summary: "0/1 buckets were imported successfully", detail: detail, exit: 1,
			})
		}
	}
	for _, mode := range []string{"text", "json"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodPut || r.URL.Path != "/minio/admin/v3/import-bucket-metadata" || r.URL.Query().Get("bucket") != "" {
						t.Errorf("unexpected import request: %s %s", r.Method, r.URL)
					}
					if r.Header.Get("Authorization") == "" {
						t.Error("unsigned import request")
					}
					data, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(data, archive.Bytes()) {
						t.Errorf("archive changed: error=%v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				args := []string{"admin", "cluster", "bucket", "import", "b4", filename}
				if mode == "json" {
					args = append([]string{"--json"}, args...)
				}
				result := runChecksumVerifyCLI(t, server.URL, nil, args...)
				if result.exitCode != tc.exit || requests.Load() != 1 {
					t.Errorf("exit=%d requests=%d want exit=%d; stdout=%s stderr=%s", result.exitCode, requests.Load(), tc.exit, result.stdout, result.stderr)
				}
				if mode == "json" {
					var want madmin.BucketMetaImportErrs
					if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
						t.Fatal(err)
					}
					var got map[string]madmin.BucketStatus
					if err := json.Unmarshal(result.stdout, &got); err != nil {
						t.Fatalf("invalid report JSON: %v: %s", err, result.stdout)
					}
					if !reflect.DeepEqual(got, want.Buckets) {
						t.Fatalf("report changed: got=%+v want=%+v", got, want.Buckets)
					}
				} else {
					output := string(result.stdout)
					if !strings.Contains(output, tc.summary) || !strings.Contains(output, tc.detail) {
						t.Fatalf("missing report details %q/%q: %s", tc.summary, tc.detail, output)
					}
				}
			})
		}
	}
}
