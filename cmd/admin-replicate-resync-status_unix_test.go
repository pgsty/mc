//go:build darwin || linux

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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Python's standard library supplies a real PTY without adding a production
// dependency. The portable UI and JSON tests also run where it is unavailable.
const resyncTTYDriver = `
import json, os, pty, select, subprocess, sys, time
master, slave = pty.openpty()
process = subprocess.Popen(sys.argv[2:], stdin=slave, stdout=slave, stderr=slave,
                           start_new_session=True)
os.close(slave)
output = bytearray()
sent_input = False
timed_out = False
deadline = time.monotonic() + 10
try:
    while True:
        if select.select([master], [], [], 0.03)[0]:
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            if not data:
                break
            output.extend(data)
        if sys.argv[1] == "ctrl-c" and not sent_input and b"Ongoing" in output:
            os.write(master, b"\x03")
            sent_input = True
        if process.poll() is not None:
            # Drain the final terminal render before closing the master.
            while select.select([master], [], [], 0.03)[0]:
                try:
                    data = os.read(master, 65536)
                except OSError:
                    break
                if not data:
                    break
                output.extend(data)
            break
        if time.monotonic() > deadline:
            timed_out = True
            break
finally:
    if timed_out:
        process.kill()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        timed_out = True
        process.kill()
        process.wait()
    os.close(master)
print(json.dumps({"output": output.decode(errors="replace"), "exit": process.returncode,
                  "timed_out": timed_out, "sent_input": sent_input}))
`

func runResyncTTYCLI(t *testing.T, endpoint, action string) (string, int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("real PTY regression requires python3")
	}
	command := checksumVerifyCLICommand(t, endpoint, []string{
		"MC_HOST_peer2=http://access:secret@" + strings.TrimPrefix(endpoint, "http://"),
		"TERM=xterm",
	}, "admin", "replicate", "resync", "status", "b4", "peer2")
	args := append([]string{"-c", resyncTTYDriver, action, command.Path}, command.Args[1:]...)
	driver := exec.CommandContext(t.Context(), python, args...)
	driver.Env = command.Env
	out, err := driver.CombinedOutput()
	if err != nil {
		t.Fatalf("PTY driver: %v: %s", err, out)
	}
	var result struct {
		Output    string `json:"output"`
		Exit      int    `json:"exit"`
		TimedOut  bool   `json:"timed_out"`
		SentInput bool   `json:"sent_input"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("PTY driver result: %v: %s", err, out)
	}
	if result.TimedOut {
		t.Fatalf("CLI stayed alive after its last event: %q", result.Output)
	}
	if action == "ctrl-c" && !result.SentInput {
		t.Fatalf("CLI quit before observing nonterminal status: %q", result.Output)
	}
	return reAnsi.ReplaceAllString(result.Output, ""), result.Exit
}

func TestAdminReplicateResyncStatusTTY(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("real PTY regression requires python3")
	}
	for _, tc := range []struct {
		name, status string
		final, tail  bool
		action       string
		marker       string
	}{
		{name: "Completed", status: "Completed", marker: tickCell},
		{name: "Canceled", status: "Canceled"},
		{name: "Failed", status: "Failed", marker: crossTickCell},
		{name: "mixed_case_failed", status: "fAiLeD", marker: crossTickCell},
		{name: "final_ongoing", status: "Ongoing", final: true},
		{name: "final_without_resync", final: true},
		{name: "buffered_tail", status: "Completed", tail: true, marker: tickCell},
		{name: "ongoing_until_ctrl_c", status: "Ongoing", action: "ctrl-c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disconnected := make(chan struct{})
			server := newResyncJSONTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				sample := resyncJSONTestSample(tc.status, 0)
				sample.Final = tc.final
				if sample.Aggregated.SiteResync != nil {
					sample.Aggregated.SiteResync.FailedCount = 0
				}
				var body bytes.Buffer
				_ = json.NewEncoder(&body).Encode(sample)
				if tc.tail {
					_ = json.NewEncoder(&body).Encode(resyncJSONTestSample("Ongoing", 1))
					body.WriteString("invalid JSON\n")
				}
				_, _ = w.Write(body.Bytes())
				w.(http.Flusher).Flush()
				// Terminal samples have final=false by default. Keep the
				// transport open until the client cancels the subscription.
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-t.Context().Done():
				}
			})
			output, code := runResyncTTYCLI(t, server.URL, tc.action)
			if code != 0 {
				t.Fatalf("query exit=%d: %q", code, output)
			}
			if tc.status != "" && (!strings.Contains(output, tc.status) || !strings.Contains(output, "resync-test")) {
				t.Errorf("lost final status: %q", output)
			}
			if tc.marker != "" && !strings.Contains(output, tc.marker) {
				t.Errorf("missing %q marker: %q", tc.marker, output)
			}
			if tc.marker != tickCell && strings.Contains(output, tickCell) {
				t.Errorf("unsuccessful resync rendered as success: %q", output)
			}
			if tc.tail && strings.Contains(output, "Ongoing") {
				t.Errorf("buffered tail replaced terminal status: %q", output)
			}
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Fatal("metrics request remained open")
			}
		})
	}
}

func TestAdminReplicateResyncStatusTTYErrors(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("real PTY regression requires python3")
	}
	for _, tc := range []struct {
		name, tail, cause string
		denied, ongoing   bool
	}{
		{name: "request_denied", denied: true, cause: "Failed to parse server response"},
		{name: "malformed", tail: "not JSON", cause: "invalid character"},
		{name: "malformed_after_sample", ongoing: true, tail: "not JSON", cause: "invalid character"},
		{name: "empty_stream", cause: "unexpected EOF"},
		{name: "eof_after_sample", ongoing: true, cause: "unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newResyncJSONTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.denied {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"Code":"AccessDenied","Message":"Access Denied."}`)
					return
				}
				if tc.ongoing {
					_ = json.NewEncoder(w).Encode(resyncJSONTestSample("Ongoing", 0))
				}
				_, _ = io.WriteString(w, tc.tail)
			})
			output, code := runResyncTTYCLI(t, server.URL, "")
			if code != 1 || !strings.Contains(output, "Unable to get resync status") || !strings.Contains(output, tc.cause) {
				t.Errorf("missing query error: exit=%d output=%q", code, output)
			}
			if tc.ongoing && !strings.Contains(output, "Ongoing") {
				t.Errorf("lost last sample before stream error: %q", output)
			}
		})
	}
}

func TestAdminReplicateResyncStatusJSONInterrupt(t *testing.T) {
	delivered := make(chan struct{})
	disconnected := make(chan struct{})
	server := newResyncJSONTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(resyncJSONTestSample("Ongoing", 0))
		w.(http.Flusher).Flush()
		close(delivered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-t.Context().Done():
		}
	})
	command := checksumVerifyCLICommand(t, server.URL, []string{
		"MC_HOST_peer2=http://access:secret@" + strings.TrimPrefix(server.URL, "http://"),
	}, "--json", "admin", "replicate", "resync", "status", "b4", "peer2")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() { _ = command.Process.Kill() })
	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("no metrics sample before interrupt")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != globalCancelExitStatus {
			t.Fatalf("interrupt exit=%v; stdout=%s stderr=%s", err, &stdout, &stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("JSON CLI did not stop after interrupt")
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("metrics stream did not disconnect after interrupt")
	}
	if bytes.Contains(stdout.Bytes(), []byte(`"status":"error"`)) || stderr.Len() != 0 {
		t.Errorf("interruption reported a query error: stdout=%s stderr=%s", &stdout, &stderr)
	}
}
