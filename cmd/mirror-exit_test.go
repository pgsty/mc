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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/minio/mc/pkg/probe"
)

func TestMirrorFailureStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		remove     bool
		listing    bool
		skipErrors bool
		wantCancel bool
		wantError  bool
	}{
		{name: "copy denied", err: PathInsufficientPermission{}, wantError: true},
		{name: "remove denied", err: PathInsufficientPermission{}, remove: true, wantError: true},
		{name: "listing denied cancels", err: PathInsufficientPermission{}, listing: true, wantCancel: true, wantError: true},
		{name: "skip listing denial", err: PathInsufficientPermission{}, listing: true, skipErrors: true, wantError: true},
		{name: "other failure cancels", err: errors.New("transfer failed"), wantCancel: true, wantError: true},
		{name: "skip other failure", err: errors.New("transfer failed"), skipErrors: true, wantError: true},
		{name: "missing source remains ignored", err: PathNotFound{}},
		{name: "success"},
	} {
		for _, summary := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/default", true: "/summary"}[summary], func(t *testing.T) {
				status := NewQuietStatus(strings.NewReader("")).(*QuietStatus)
				status.SetTotal(23)
				status.Add(7)
				job := &mirrorJob{status: status, statusCh: make(chan URLs), opts: mirrorOptions{isSummary: summary, skipErrors: tc.skipErrors}}
				content := &ClientContent{URL: *newClientURL("/object")}
				result := URLs{SourceContent: content, Error: probe.NewError(tc.err)}
				if tc.remove {
					result.SourceContent, result.TargetContent = nil, content
				}
				if tc.listing {
					result.SourceContent = nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				out := captureCommandJSON(t, func() {
					done := make(chan bool, 1)
					go func() { done <- job.monitorMirrorStatus(cancel) }()
					job.statusCh <- result
					// This handoff proves the previous result has been processed.
					job.statusCh <- URLs{SourceContent: content}
					if (ctx.Err() != nil) != tc.wantCancel {
						t.Errorf("early cancellation=%v, want %v", ctx.Err(), tc.wantCancel)
					}
					close(job.statusCh)
					if failed := <-done; failed != tc.wantError {
						t.Errorf("failed=%v want=%v", failed, tc.wantError)
					}
				})
				select {
				case <-status.isFinished:
				default:
					t.Error("accounter was not stopped")
				}
				if tc.wantError && !strings.Contains(out, `"status":"error"`) {
					t.Errorf("missing error diagnostic: %s", out)
				}
				if tc.wantError && !summary {
					if strings.Contains(out, `"total":`) {
						t.Errorf("unexpected failed summary: %s", out)
					}
					return
				}
				lines := strings.Split(strings.TrimSpace(out), "\n")
				var stat accountStat
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &stat); err != nil {
					t.Fatal(err)
				}
				wantStatus := "success"
				if tc.wantError {
					wantStatus = "failure"
				}
				if stat.Status != wantStatus || stat.Total != 23 || stat.Transferred != 7 {
					t.Errorf("incorrect or twice-read statistics: %+v", stat)
				}
			})
		}
	}
}

func TestAccountStatJSONStatus(t *testing.T) {
	for _, status := range []string{"", "failure"} {
		stat := accountStat{Status: status, Total: 23, Transferred: 7, Duration: time.Second, Speed: 7}
		want := stat
		if want.Status == "" {
			want.Status = "success"
		}
		var got accountStat
		if err := json.Unmarshal([]byte(stat.JSON()), &got); err != nil || got != want {
			t.Fatalf("got=%+v want=%+v error=%v", got, want, err)
		}
	}
}

func TestMirrorPermissionFailureCLI(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions and an unprivileged user")
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json", []string{"--json", "mirror"}},
		{"json summary", []string{"--json", "mirror", "--summary"}},
		{"text summary", []string{"mirror", "--summary"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			denied := filepath.Join(source, "a-denied")
			if err := os.WriteFile(denied, []byte("denied"), 0o000); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(denied, 0o600)
			if err := os.WriteFile(filepath.Join(source, "z-allowed"), []byte("allowed"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append(append([]string{}, tc.args...), "--max-workers", "1", source, target)
			result := runChecksumVerifyCLI(t, "http://localhost", nil, args...)
			data, err := os.ReadFile(filepath.Join(target, "z-allowed"))
			if result.exitCode != 1 || err != nil || string(data) != "allowed" {
				t.Fatalf("exit=%d data=%q error=%v stdout=%s stderr=%s", result.exitCode, data, err, result.stdout, result.stderr)
			}
			if _, err = os.Stat(filepath.Join(target, "a-denied")); !os.IsNotExist(err) {
				t.Fatalf("unreadable file was copied: %v", err)
			}
			if tc.name == "text summary" {
				if !bytes.Contains(result.stderr, []byte("Failed to copy")) || !bytes.Contains(result.stdout, []byte("Transferred")) {
					t.Fatalf("missing diagnostic or summary: stdout=%s stderr=%s", result.stdout, result.stderr)
				}
			} else if !bytes.Contains(result.stdout, []byte(`"status":"error"`)) {
				t.Fatalf("missing error: %s", result.stdout)
			}
		})
	}
	t.Run("unreadable source directory", func(t *testing.T) {
		source, target := t.TempDir(), t.TempDir()
		denied := filepath.Join(source, "denied")
		if err := os.Mkdir(denied, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(denied, 0o700)
		result := runChecksumVerifyCLI(t, "http://localhost", nil, "--json", "mirror", "--summary", source, target)
		if result.exitCode != 1 || !bytes.Contains(result.stdout, []byte(`"status":"failure"`)) {
			t.Fatalf("incomplete listing: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
	})
	t.Run("remove denied", func(t *testing.T) {
		source, target := t.TempDir(), t.TempDir()
		orphan := filepath.Join(target, "orphan")
		if err := os.WriteFile(orphan, []byte("retained"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(target, 0o700)
		result := runChecksumVerifyCLI(t, "http://localhost", nil, "--json", "mirror", "--remove", source, target)
		if result.exitCode != 1 || !bytes.Contains(result.stdout, []byte("Failed to remove")) {
			t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if _, err := os.Stat(orphan); err != nil {
			t.Fatalf("denied deletion changed the target: %v", err)
		}
	})
}
