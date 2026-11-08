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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRmPurgeDryRunDoesNotDeleteLocalFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil,
		"--json", "rm", "--force", "--purge", "--dry-run", file)
	if result.exitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "keep" {
		t.Fatalf("dry run changed source: data=%q error=%v", got, err)
	}
	var message rmMessage
	if err := json.Unmarshal(result.stdout, &message); err != nil || !message.DryRun {
		t.Fatalf("dry-run result did not identify itself: %s", result.stdout)
	}
}

func TestRmNamespaceGuardChecksEveryTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"bucket-first", []string{"b4/bucket/", "b4/"}, ""},
		{"namespace-first", []string{"b4/", "b4/bucket/"}, ""},
		{"normalized", []string{"b4/bucket/", "b4/."}, ""},
		{"stdin", nil, "b4/\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "must reject before contacting storage", http.StatusForbidden)
			}))
			defer server.Close()
			args := []string{"--json", "rm", "--recursive", "--force"}
			if tc.stdin != "" {
				args = append(args, "--stdin")
			}
			args = append(args, tc.args...)
			command := checksumVerifyCLICommand(t, server.URL, nil, args...)
			command.Stdin = strings.NewReader(tc.stdin)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if code := checksumVerifyCLIExitCode(t, command); code != 1 || !strings.Contains(output.String(), "site-wide") {
				t.Fatalf("exit=%d output=%s", code, &output)
			}
			if requests.Load() != 0 {
				t.Fatalf("contacted storage before rejection: %d", requests.Load())
			}
		})
	}
}

func TestRmPurgeDryRunDoesNotDeleteRemotePrefix(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "dry-run must not delete", http.StatusForbidden)
	}))
	defer server.Close()
	result := runChecksumVerifyCLI(t, server.URL, nil, "--json", "rm", "--force", "--purge", "--dry-run", "b4/bucket/prefix/")
	if result.exitCode != 0 || requests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d stdout=%s stderr=%s", result.exitCode, requests.Load(), result.stdout, result.stderr)
	}
	var msg rmMessage
	if err := json.Unmarshal(result.stdout, &msg); err != nil || !msg.DryRun || msg.Key != "b4/bucket/prefix/" {
		t.Fatalf("unexpected purge preview: %s error=%v", result.stdout, err)
	}
}

func TestRmStdinHonorsDangerous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>`)
	}))
	defer server.Close()
	command := checksumVerifyCLICommand(t, server.URL, nil, "--json", "rm", "--recursive", "--force", "--dangerous", "--stdin")
	command.Stdin = strings.NewReader("b4/\n")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if code := checksumVerifyCLIExitCode(t, command); code != 0 {
		t.Fatalf("exit=%d output=%s", code, &output)
	}
}
