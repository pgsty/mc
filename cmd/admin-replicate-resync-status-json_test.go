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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
)

func newResyncJSONTestServer(t *testing.T, metrics http.HandlerFunc) *httptest.Server {
	t.Helper()
	var siteRequests, peerRequests, metricsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/minio/admin/v3/site-replication/info":
			siteRequests.Add(1)
			_, _ = io.WriteString(w, `{"enabled":true,"sites":[{"deploymentID":"peer-site"}]}`)
		case "/minio/admin/v3/info":
			peerRequests.Add(1)
			_, _ = io.WriteString(w, `{"deploymentID":"peer-site","servers":[]}`)
		case "/minio/admin/v3/metrics":
			metricsRequests.Add(1)
			if q := r.URL.Query(); q.Get("by-depID") != "peer-site" || q.Get("types") != strconv.Itoa(int(madmin.MetricsSiteResync)) || q.Get("n") != "0" {
				t.Errorf("unexpected metrics options: %s", r.URL)
			}
			metrics(w, r)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if siteRequests.Load() != 1 || peerRequests.Load() != 1 || metricsRequests.Load() != 1 {
			t.Errorf("requests: site=%d peer=%d metrics=%d, want one each", siteRequests.Load(), peerRequests.Load(), metricsRequests.Load())
		}
	})
	return server
}

func resyncJSONTestSample(status string, sequence int) madmin.RealtimeMetrics {
	metrics := madmin.RealtimeMetrics{Hosts: []string{"silo1"}}
	if status == "" {
		return metrics
	}
	start := time.Unix(100, 0).UTC()
	metrics.Aggregated.SiteResync = &madmin.SiteResyncMetrics{
		ResyncID:        "resync-test",
		DeplID:          "peer-site",
		ResyncStatus:    status,
		StartTime:       start,
		LastUpdate:      start.Add(time.Duration(sequence+1) * time.Second),
		ReplicatedSize:  int64(sequence+1) << 30,
		ReplicatedCount: int64(sequence + 1),
		FailedCount:     1,
		FailedBuckets:   []string{"failed-bucket"},
		Bucket:          "test-bucket",
		Object:          fmt.Sprintf("object-%d", sequence),
	}
	return metrics
}

type resyncJSONCLI struct {
	records <-chan json.RawMessage
	done    chan struct{}
	err     error
	stderr  bytes.Buffer
}

// The CLI runs with /dev/null as stdin and pipes for output, without a TTY.
// Decode while it is running so the server can wait for each observed sample.
func startResyncJSONCLI(t *testing.T, endpoint string, leafFlag bool) *resyncJSONCLI {
	t.Helper()
	args := []string{"admin", "replicate", "resync", "status", "b4", "peer2"}
	if leafFlag {
		args = append(args, "--json")
	} else {
		args = append([]string{"--json"}, args...)
	}
	command := checksumVerifyCLICommand(t, endpoint, []string{
		"MC_HOST_peer2=http://access:secret@" + strings.TrimPrefix(endpoint, "http://"),
	}, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	records := make(chan json.RawMessage, 16)
	process := &resyncJSONCLI{records: records, done: make(chan struct{})}
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		decoder := json.NewDecoder(stdout)
		for {
			var record json.RawMessage
			if err := decoder.Decode(&record); err != nil {
				if !errors.Is(err, io.EOF) {
					process.err = fmt.Errorf("invalid CLI JSON: %w", err)
					_ = command.Process.Kill()
				}
				break
			}
			select {
			case records <- record:
			case <-t.Context().Done():
			}
		}
		close(records)
		waitErr := command.Wait()
		if process.err == nil {
			process.err = waitErr
		}
		close(process.done)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			t.Error("CLI did not stop after being killed")
		}
	})
	return process
}

func (p *resyncJSONCLI) next(t *testing.T) json.RawMessage {
	t.Helper()
	select {
	case record, ok := <-p.records:
		if !ok {
			t.Fatal("CLI exited without the expected JSON record")
		}
		return record
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for streaming JSON output")
		return nil
	}
}

func (p *resyncJSONCLI) finish(t *testing.T, wantExit int) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("CLI did not exit after the stream finished")
	}
	exitCode := 0
	if p.err != nil {
		var exitErr *exec.ExitError
		if !errors.As(p.err, &exitErr) {
			t.Fatal(p.err)
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != wantExit {
		t.Errorf("exit=%d, want %d; stderr=%q", exitCode, wantExit, p.stderr.String())
	}
	for record := range p.records {
		t.Errorf("unexpected JSON after last event: %s", record)
	}
	if p.stderr.Len() != 0 {
		t.Errorf("unexpected stderr: %q", p.stderr.String())
	}
}

func TestAdminReplicateResyncStatusJSONStream(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		leafFlag bool
		final    bool
	}{
		{name: "completed_root_flag", statuses: []string{"", "Pending", "Ongoing", "Ongoing", "Completed"}},
		{name: "completed_leaf_flag", statuses: []string{"", "Pending", "Ongoing", "Ongoing", "Completed"}, leafFlag: true},
		{name: "canceled", statuses: []string{"Ongoing", "Canceled"}},
		{name: "failed", statuses: []string{"Ongoing", "Failed"}},
		{name: "already_completed", statuses: []string{"Completed"}},
		{name: "already_canceled", statuses: []string{"Canceled"}},
		{name: "already_failed", statuses: []string{"Failed"}},
		{name: "lowercase_completed", statuses: []string{"completed"}},
		{name: "lowercase_canceled", statuses: []string{"canceled"}},
		{name: "mixed_case_failed", statuses: []string{"fAiLeD"}},
		{name: "final_packet", statuses: []string{"Ongoing"}, final: true},
		{name: "final_packet_without_resync", statuses: []string{""}, final: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan struct{})
			disconnected := make(chan struct{})
			server := newResyncJSONTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				for i, status := range tc.statuses {
					sample := resyncJSONTestSample(status, i)
					sample.Final = tc.final
					if err := json.NewEncoder(w).Encode(sample); err != nil {
						t.Errorf("writing sample: %v", err)
						return
					}
					w.(http.Flusher).Flush()
					if i < len(tc.statuses)-1 {
						select {
						case <-observed:
						case <-r.Context().Done():
							return
						case <-t.Context().Done():
							return
						}
					}
				}
				// Keep the response open after the terminal status. The client
				// must cancel its subscription, not wait for EOF or Final=true.
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-t.Context().Done():
				}
			})
			process := startResyncJSONCLI(t, server.URL, tc.leafFlag)
			for i, status := range tc.statuses {
				var got madmin.RealtimeMetrics
				if err := json.Unmarshal(process.next(t), &got); err != nil {
					t.Fatal(err)
				}
				want := resyncJSONTestSample(status, i)
				want.Final = tc.final
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("sample %d: got %+v, want %+v", i, got, want)
				}
				if i < len(tc.statuses)-1 {
					select {
					case observed <- struct{}{}:
					case <-time.After(10 * time.Second):
						t.Fatal("server stopped before the terminal sample")
					}
				}
			}
			process.finish(t, 0)
			select {
			case <-disconnected:
			case <-time.After(10 * time.Second):
				t.Fatal("metrics subscription remained open after terminal status")
			}
		})
	}
}

func TestAdminReplicateResyncStatusJSONBufferedTail(t *testing.T) {
	server := newResyncJSONTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// A decoder may have read ahead before cancellation. Nothing after a
		// successfully printed terminal event belongs to this subscription.
		var body bytes.Buffer
		_ = json.NewEncoder(&body).Encode(resyncJSONTestSample("Completed", 0))
		_ = json.NewEncoder(&body).Encode(resyncJSONTestSample("Ongoing", 1))
		body.WriteString("invalid JSON\n")
		_, _ = w.Write(body.Bytes())
	})
	process := startResyncJSONCLI(t, server.URL, false)
	var got madmin.RealtimeMetrics
	if err := json.Unmarshal(process.next(t), &got); err != nil {
		t.Fatal(err)
	}
	if want := resyncJSONTestSample("Completed", 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	process.finish(t, 0)
}

func TestAdminReplicateResyncStatusJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		denied    bool
		ongoing   bool
		transport bool
		tail      string
		wantCause string
	}{
		{name: "metrics_forbidden", denied: true, wantCause: "403 Forbidden"},
		{name: "malformed_json", tail: "not JSON", wantCause: "invalid character"},
		{name: "malformed_json_after_sample", ongoing: true, tail: "not JSON", wantCause: "invalid character"},
		{name: "truncated_json", tail: `{"aggregated":`, wantCause: "unexpected EOF"},
		{name: "empty_stream", wantCause: "unexpected EOF"},
		{name: "unexpected_eof_after_sample", ongoing: true, wantCause: "unexpected EOF"},
		{name: "invalid_chunked_transfer", transport: true, wantCause: "invalid byte in chunk length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newResyncJSONTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.transport {
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijacking metrics response: %v", err)
						return
					}
					defer conn.Close()
					_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\ninvalid\r\n")
					_ = rw.Flush()
					return
				}
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
			process := startResyncJSONCLI(t, server.URL, false)
			if tc.ongoing {
				var got madmin.RealtimeMetrics
				if err := json.Unmarshal(process.next(t), &got); err != nil {
					t.Fatal(err)
				}
				if want := resyncJSONTestSample("Ongoing", 0); !reflect.DeepEqual(got, want) {
					t.Fatalf("lost sample before failure: %+v", got)
				}
			}
			record := process.next(t)
			var got struct {
				Status string       `json:"status"`
				Error  errorMessage `json:"error"`
			}
			if err := json.Unmarshal(record, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != "error" || got.Error.Message != "Unable to get resync status" || got.Error.Cause.Message == "" || !bytes.Contains(record, []byte(tc.wantCause)) {
				t.Errorf("missing resync request error (%s): %s", tc.wantCause, record)
			}
			process.finish(t, 1)
		})
	}
}
