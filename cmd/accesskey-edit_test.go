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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/minio/madmin-go/v3"
)

func TestAccesskeyEditPolicyCLI(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"admin", []string{"admin", "accesskey", "edit"}},
		{"ldap", []string{"idp", "ldap", "accesskey", "edit"}},
		{"openid", []string{"idp", "openid", "accesskey", "edit"}},
	}
	cases := []struct {
		name, document, wantError string
		omitPolicy, missingFile   bool
	}{
		{name: "omitted policy", omitPolicy: true},
		{name: "empty object", document: `{}`},
		{name: "empty statements", document: `{"Statement":[]}`},
		{name: "versioned empty statements", document: `{"Version":"2012-10-17","Statement":[]}`},
		{name: "inline policy", document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`},
		{name: "inline policy without version", document: `{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`},
		{name: "missing file", missingFile: true, wantError: "unable to read the policy document"},
		{name: "empty file", wantError: "unable to parse the policy document"},
		{name: "whitespace", document: " \n\t", wantError: "unable to parse the policy document"},
		{name: "malformed JSON", document: `{"Statement":`, wantError: "unable to parse the policy document"},
		{name: "null", document: `null`, wantError: "unable to parse the policy document"},
		{name: "array", document: `[]`, wantError: "unable to parse the policy document"},
		{name: "unknown field", document: `{"Statment":[]}`, wantError: "unable to parse the policy document"}, //nolint:misspell // Intentionally invalid policy field tests strict parsing.
		{name: "trailing document", document: `{} {}`, wantError: "unable to parse the policy document"},
		{name: "trailing garbage", document: `{} invalid`, wantError: "unable to parse the policy document"},
		{name: "invalid version", document: `{"Version":"invalid","Statement":[]}`, wantError: "unable to parse the policy document"},
		{name: "invalid statement type", document: `{"Statement":1}`, wantError: "unable to parse the policy document"},
		{name: "invalid statement", document: `{"Statement":[{}]}`, wantError: "unable to parse the policy document"},
		{name: "bare ARN", document: `{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::"]}]}`, wantError: "unable to parse the policy document"},
		{name: "conflicting resources", document: `{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"],"NotResource":["arn:aws:s3:::other/*"]}]}`, wantError: "unable to parse the policy document"},
	}
	for _, command := range commands {
		for _, tc := range cases {
			t.Run(command.name+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if tc.wantError != "" {
						t.Error("invalid policy reached the server")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/minio/admin/v3/update-service-account" || r.URL.Query().Get("accessKey") != "test-access-key" {
						t.Errorf("unexpected edit request: %s %s", r.Method, r.URL)
					}
					if r.Header.Get("Authorization") == "" {
						t.Error("unsigned edit request")
					}
					body, err := madmin.DecryptData("secret", r.Body)
					if err != nil {
						t.Errorf("decrypt edit request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var request map[string]json.RawMessage
					if err := json.Unmarshal(body, &request); err != nil {
						t.Errorf("decode edit request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if tc.omitPolicy {
						if _, ok := request["newPolicy"]; ok {
							t.Errorf("omitted --policy must not change the policy: %s", body)
						}
						if string(request["newName"]) != `"renamed"` {
							t.Errorf("name edit lost: %s", body)
						}
					} else if string(request["newPolicy"]) != tc.document {
						t.Errorf("policy changed: got %s, want %s", request["newPolicy"], tc.document)
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()

				args := append([]string{}, command.args...)
				if tc.omitPolicy {
					args = append(args, "--name", "renamed")
				} else {
					filename := filepath.Join(t.TempDir(), "policy.json")
					if !tc.missingFile {
						if err := os.WriteFile(filename, []byte(tc.document), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					args = append(args, "--policy", filename)
				}
				args = append(args, "b4", "test-access-key")
				result := runChecksumVerifyCLI(t, server.URL, nil, args...)
				if tc.wantError != "" {
					output := string(result.stdout) + string(result.stderr)
					if result.exitCode != 1 || requests.Load() != 0 || !strings.Contains(output, tc.wantError) {
						t.Fatalf("exit=%d requests=%d, want local rejection containing %q; output=%s", result.exitCode, requests.Load(), tc.wantError, output)
					}
				} else if result.exitCode != 0 || requests.Load() != 1 {
					t.Fatalf("exit=%d requests=%d, want success with one edit request; stdout=%s stderr=%s", result.exitCode, requests.Load(), result.stdout, result.stderr)
				}
			})
		}
	}
}

func TestNamedPolicyWriteCLI(t *testing.T) {
	cases := []struct {
		name, document, wantError string
	}{
		{name: "valid policy", document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`},
		{name: "empty object", document: `{}`, wantError: "empty policies are not allowed"},
		{name: "empty statements", document: `{"Statement":[]}`, wantError: "empty policies are not allowed"},
		{name: "versioned empty statements", document: `{"Version":"2012-10-17","Statement":[]}`, wantError: "empty policies are not allowed"},
		{name: "missing version", document: `{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`, wantError: "policy version cannot be empty"},
		{name: "malformed JSON", document: `{"Statement":`, wantError: "Unable to parse policy"},
		{name: "unknown field", document: `{"Statment":[]}`, wantError: "unknown field"}, //nolint:misspell // Intentionally invalid policy field tests strict parsing.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.wantError != "" {
					t.Error("invalid named policy reached the server")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Method != http.MethodPut || r.URL.Path != "/minio/admin/v3/add-canned-policy" || r.URL.Query().Get("name") != "test-policy" {
					t.Errorf("unexpected policy request: %s %s", r.Method, r.URL)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.document {
					t.Errorf("policy changed: got %s, want %s; error=%v", body, tc.document, err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			filename := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(filename, []byte(tc.document), 0o600); err != nil {
				t.Fatal(err)
			}
			result := runChecksumVerifyCLI(t, server.URL, nil, "admin", "policy", "create", "b4", "test-policy", filename)
			if tc.wantError != "" {
				output := string(result.stdout) + string(result.stderr)
				if result.exitCode != 1 || requests.Load() != 0 || !strings.Contains(output, tc.wantError) {
					t.Fatalf("exit=%d requests=%d, want local rejection containing %q; output=%s", result.exitCode, requests.Load(), tc.wantError, output)
				}
			} else if result.exitCode != 0 || requests.Load() != 1 {
				t.Fatalf("exit=%d requests=%d, want success with one policy request; stdout=%s stderr=%s", result.exitCode, requests.Load(), result.stdout, result.stderr)
			}
		})
	}
}
