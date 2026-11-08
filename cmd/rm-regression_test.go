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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/minio/mc/pkg/probe"
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

// POSIX parent-directory permissions control unlink; Windows ACLs and root
// bypass these mode bits and need a separate platform-specific fixture.
func TestRmPermissionFailuresCLI(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions and a non-root user")
	}
	for _, mode := range []string{"text", "json"} {
		for _, kind := range []string{"single", "recursive", "listing"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				deniedDir := filepath.Join(root, "a-denied")
				if err := os.Mkdir(deniedDir, 0o700); err != nil {
					t.Fatal(err)
				}
				deniedFile := filepath.Join(deniedDir, "keep")
				allowedFile := filepath.Join(root, "z-delete")
				for _, name := range []string{deniedFile, allowedFile} {
					if err := os.WriteFile(name, []byte("payload"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				permission := os.FileMode(0o500)
				if kind == "listing" {
					permission = 0
				}
				if err := os.Chmod(deniedDir, permission); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(deniedDir, 0o700) })
				args := []string{"rm"}
				if mode == "json" {
					args = append([]string{"--json"}, args...)
				}
				if kind == "single" {
					args = append(args, deniedFile, allowedFile)
				} else {
					args = append(args, "--recursive", "--force", root+string(os.PathSeparator))
				}
				result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
				if result.exitCode != 1 {
					t.Errorf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
				}
				if err := os.Chmod(deniedDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(deniedFile); err != nil || string(data) != "payload" {
					t.Errorf("failed removal changed file: data=%q error=%v", data, err)
				}
				if _, err := os.Stat(allowedFile); !os.IsNotExist(err) {
					t.Errorf("independent file not removed: %v", err)
				}
				if mode == "json" {
					decoder := json.NewDecoder(bytes.NewReader(result.stdout))
					var failed, succeeded bool
					for {
						var record struct{ Status, Key string }
						if err := decoder.Decode(&record); err == io.EOF {
							break
						} else if err != nil {
							t.Fatal(err)
						}
						failed = failed || record.Status == "error"
						succeeded = succeeded || (record.Status == "success" && record.Key == allowedFile)
					}
					if !failed || !succeeded {
						t.Errorf("missing per-target results: %s", result.stdout)
					}
				}
			})
		}
	}
}

func TestRmForceKeepsListingPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions and a non-root user")
	}
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			denied := filepath.Join(root, "denied")
			if err := os.Mkdir(denied, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(denied, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(denied, 0o700) })
			// No files can be enumerated; --force must retain the listing failure.
			args := []string{"rm", "--recursive", "--force", denied + string(os.PathSeparator)}
			if mode == "json" {
				args = append([]string{"--json"}, args...)
			}
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
			if result.exitCode != 1 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			if _, err := os.Stat(denied); err != nil {
				t.Fatalf("failed listing removed directory: %v", err)
			}
		})
	}
}

func TestRmForceMissingPrefixStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `<ListBucketResult><Name>bucket</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
		} else if r.Method != http.MethodHead {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	for _, mode := range []string{"text", "json"} {
		args := []string{"rm", "--recursive", "--force", "b4/bucket/missing/"}
		if mode == "json" {
			args = append([]string{"--json"}, args...)
		}
		result := runChecksumVerifyCLI(t, server.URL, []string{"MC_REGION=us-east-1"}, args...)
		if result.exitCode != 0 {
			t.Fatalf("%s exit=%d stdout=%s stderr=%s", mode, result.exitCode, result.stdout, result.stderr)
		}
	}
}

// Exercise the error-only listing contract independently of directory records
// emitted by the filesystem walker.
type rmDeniedListingClient struct{ Client }

func (rmDeniedListingClient) List(context.Context, ListOptions) <-chan *ClientContent {
	ch := make(chan *ClientContent, 1)
	ch <- &ClientContent{Err: probe.NewError(PathInsufficientPermission{Path: "denied"})}
	close(ch)
	return ch
}

func (rmDeniedListingClient) Remove(_ context.Context, _, _, _, _ bool, entries <-chan *ClientContent) <-chan RemoveResult {
	results := make(chan RemoveResult)
	go func() {
		defer close(results)
		for range entries {
		}
	}()
	return results
}

func TestRmForceErrorOnlyListing(t *testing.T) {
	oldNew, oldLoad := S3New, loadMcConfig
	defer func() { S3New, loadMcConfig = oldNew, oldLoad }()
	loadMcConfig = func() (*configV10, *probe.Error) { return &configV10{}, nil }
	t.Setenv("MC_HOST_rmdenied", "http://access:secret@localhost")
	S3New = func(*Config) (Client, *probe.Error) { return rmDeniedListingClient{}, nil }
	for _, dryRun := range []bool{false, true} {
		captureCommandJSON(t, func() {
			err := listAndRemove("rmdenied/bucket/", removeOpts{isForce: true, isRecursive: true, isFake: dryRun})
			if err == nil {
				t.Errorf("dryRun=%v: --force erased a listing failure", dryRun)
			}
		})
	}
}
