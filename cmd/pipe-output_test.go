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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runPipeCLI streams stdin into a real mcli subprocess whose stdout is a pipe,
// which is what a redirect or a shell pipeline looks like to the process. It
// reuses the checksum verify CLI harness, so the child runs Main with a clean
// MC_* environment; the endpoint is never contacted because the target is a
// local path.
func runPipeCLI(t *testing.T, stdin io.Reader, args ...string) checksumVerifyCLIResult {
	t.Helper()
	command := checksumVerifyCLICommand(t, "http://127.0.0.1:1", nil, args...)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	exitCode := checksumVerifyCLIExitCode(t, command)
	return checksumVerifyCLIResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

// trickleReader hands out its payload a few bytes at a time with a pause in
// between, so the progress bar has time to redraw during the upload. Without
// it a local target finishes inside one refresh interval and the absence of
// bar frames would prove nothing.
type trickleReader struct {
	chunks [][]byte
	pause  time.Duration
}

func (r *trickleReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

// TestPipeOutputContract pins what `mcli pipe` writes to a stdout that is not
// a terminal: the summary, in the form the flags ask for, and nothing else.
// Progress redraws belong to the terminal UI and must not reach a captured
// stream, which is the rule pgsty/mc#5 settled for this global.
func TestPipeOutputContract(t *testing.T) {
	const payload = "silo pipe output contract"

	// Main raises quiet for a stdout with no window size on every platform but
	// Windows, so only there does a run without an explicit flag depend on it.
	autoQuiet := runtime.GOOS != "windows"

	for _, tc := range []struct {
		name     string
		appFlags []string
		cmdFlags []string
		wantJSON bool
		wantText bool
		explicit bool // a flag suppresses the bar without help from auto-quiet
	}{
		{name: "no flags", wantText: true},
		{name: "json before command", appFlags: []string{"--json"}, wantJSON: true, explicit: true},
		{name: "json after command", cmdFlags: []string{"--json"}, wantJSON: true, explicit: true},
		{name: "quiet before command", appFlags: []string{"--quiet"}, wantText: true, explicit: true},
		{name: "quiet after command", cmdFlags: []string{"--quiet"}, wantText: true, explicit: true},
		{name: "explicit json false", cmdFlags: []string{"--json=false"}, wantText: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "object")
			args := append([]string{}, tc.appFlags...)
			args = append(args, "pipe")
			args = append(args, tc.cmdFlags...)
			args = append(args, target)

			result := runPipeCLI(t, strings.NewReader(payload), args...)
			if result.exitCode != 0 {
				t.Fatalf("exit code %d, want 0; stderr=%s", result.exitCode, result.stderr)
			}
			if got, e := os.ReadFile(target); e != nil || string(got) != payload {
				t.Fatalf("target = %q, %v; want %q", got, e, payload)
			}
			if (tc.explicit || autoQuiet) && bytes.ContainsRune(result.stdout, '\r') {
				t.Fatalf("progress redraw reached a captured stdout: %q", result.stdout)
			}
			switch {
			case tc.wantJSON:
				var msg pipeMessage
				if e := json.Unmarshal(bytes.TrimSpace(result.stdout), &msg); e != nil {
					t.Fatalf("stdout is not one JSON document: %v in %q", e, result.stdout)
				}
				if msg.Status != "success" || msg.Size != int64(len(payload)) {
					t.Fatalf("JSON summary = %+v, want success and %d bytes", msg, len(payload))
				}
			case tc.wantText:
				if !strings.Contains(string(result.stdout), "bytes -> ") {
					t.Fatalf("stdout %q does not carry the human summary", result.stdout)
				}
			}
		})
	}

	t.Run("no redraw during a slow upload", func(t *testing.T) {
		if !autoQuiet {
			t.Skip("Main does not raise auto-quiet on this platform, so the bar is expected here")
		}
		target := filepath.Join(t.TempDir(), "object")
		stdin := &trickleReader{
			chunks: [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cccc")},
			pause:  400 * time.Millisecond,
		}
		result := runPipeCLI(t, stdin, "pipe", target)
		if result.exitCode != 0 {
			t.Fatalf("exit code %d, want 0; stderr=%s", result.exitCode, result.stderr)
		}
		if n := bytes.Count(result.stdout, []byte{'\r'}); n != 0 {
			t.Fatalf("%d progress redraws reached a captured stdout: %q", n, result.stdout)
		}
		if got, e := os.ReadFile(target); e != nil || string(got) != "aaaabbbbcccc" {
			t.Fatalf("target = %q, %v", got, e)
		}
	})
}
