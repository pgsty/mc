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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type batchStatusFixture struct {
	describe http.HandlerFunc
	metrics  http.HandlerFunc
	status   http.HandlerFunc
}

func batchStatusNoSuchJob(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"Code":"XMinioAdminNoSuchJob","Message":"The specified job does not exist."}`)
}

func newBatchStatusServer(t *testing.T, fixture batchStatusFixture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("status observation must not mutate jobs: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		handler := fixture.describe
		switch r.URL.Path {
		case "/minio/admin/v3/describe-job":
			if handler == nil {
				handler = func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "version: v1\n")
				}
			}
		case "/minio/admin/v3/status-job":
			handler = fixture.status
			if handler == nil {
				handler = batchStatusNoSuchJob
			}
		case "/minio/admin/v3/metrics":
			handler = fixture.metrics
			q := r.URL.Query()
			if q.Get("by-jobID") != "job" || q.Get("types") != strconv.Itoa(int(madmin.MetricsBatchJobs)) || q.Get("n") != "0" {
				t.Errorf("unexpected metrics options: %s", r.URL)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.URL.Path != "/minio/admin/v3/metrics" && r.URL.Query().Get("jobId") != "job" {
			t.Errorf("unexpected job ID: %s", r.URL)
		}
		if handler == nil {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func batchStatusTestMetric(status string) madmin.JobMetric {
	return madmin.JobMetric{
		JobID:      "job",
		JobType:    string(madmin.BatchJobReplicate),
		StartTime:  time.Unix(100, 0).UTC(),
		LastUpdate: time.Unix(102, 0).UTC(),
		Complete:   status == "complete",
		Failed:     status == "failed",
		Replicate:  &madmin.ReplicateInfo{Objects: 2, BytesTransferred: 1024},
	}
}

func batchStatusTestSample(status string) madmin.RealtimeMetrics {
	sample := madmin.RealtimeMetrics{}
	if status == "nil" {
		return sample
	}
	sample.Aggregated.BatchJobs = &madmin.BatchJobMetrics{Jobs: make(map[string]madmin.JobMetric)}
	if status == "empty" {
		return sample
	}
	if status == "other" {
		other := batchStatusTestMetric("complete")
		other.JobID = "other"
		sample.Aggregated.BatchJobs.Jobs["other"] = other
		return sample
	}
	sample.Aggregated.BatchJobs.Jobs["job"] = batchStatusTestMetric(status)
	return sample
}

func runBatchStatusCLI(t *testing.T, endpoint string, jsonMode bool) checksumVerifyCLIResult {
	t.Helper()
	args := []string{"batch", "status", "b4", "job"}
	if jsonMode {
		args = append([]string{"--json"}, args...)
	}
	command := checksumVerifyCLICommand(t, endpoint, nil, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatalf("batch status hung: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return checksumVerifyCLIResult{exitCode: code, stdout: stdout.Bytes(), stderr: stderr.Bytes()}
}

func batchStatusRecords(t *testing.T, output []byte) []batchJobStatusMessage {
	t.Helper()
	var records []batchJobStatusMessage
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var record batchJobStatusMessage
		if err := decoder.Decode(&record); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("invalid JSON output %q: %v", output, err)
			}
			return records
		}
		records = append(records, record)
	}
}

func TestBatchStatusCLIWaitsForMetrics(t *testing.T) {
	for _, empty := range []string{"empty", "nil", "other"} {
		for _, terminal := range []string{"complete", "failed"} {
			for _, jsonMode := range []bool{true, false} {
				t.Run(empty+"/"+terminal+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
					var delivered atomic.Int32
					disconnected := make(chan struct{})
					server := newBatchStatusServer(t, batchStatusFixture{metrics: func(w http.ResponseWriter, r *http.Request) {
						defer close(disconnected)
						for i, status := range []string{empty, "in-progress", terminal} {
							if i > 0 {
								select {
								case <-time.After(150 * time.Millisecond):
								case <-r.Context().Done():
									return
								}
							}
							_ = json.NewEncoder(w).Encode(batchStatusTestSample(status))
							w.(http.Flusher).Flush()
							delivered.Add(1)
						}
						<-r.Context().Done()
					}})
					result := runBatchStatusCLI(t, server.URL, jsonMode)
					if result.exitCode != 0 || len(result.stderr) != 0 {
						t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
					}
					if delivered.Load() != 3 {
						t.Errorf("disconnected before terminal result: delivered %d samples", delivered.Load())
					}
					select {
					case <-disconnected:
					case <-time.After(time.Second):
						t.Error("metrics subscription not canceled")
					}
					if jsonMode {
						records := batchStatusRecords(t, result.stdout)
						var statuses []string
						for _, record := range records {
							statuses = append(statuses, record.Status)
						}
						if !reflect.DeepEqual(statuses, []string{"pending", "in-progress", terminal}) {
							t.Fatalf("statuses=%v, want pending, in-progress, %s; stdout=%s", statuses, terminal, result.stdout)
						}
						if !reflect.DeepEqual(records[2].Metric, batchStatusTestMetric(terminal)) {
							t.Errorf("terminal metric changed: %+v", records[2])
						}
					} else {
						text := string(result.stdout)
						if !strings.Contains(text, "pending") || !strings.Contains(text, "in-progress") || strings.Count(text, terminal) != 1 || strings.Contains(text, "\x1b") {
							t.Errorf("missing or duplicated text states: %q", text)
						}
					}
				})
			}
		}
	}
}

func assertBatchStatusResult(t *testing.T, result checksumVerifyCLIResult, jsonMode bool, wantExit int, wantStatus, wantCause string) {
	t.Helper()
	if result.exitCode != wantExit {
		t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", result.exitCode, wantExit, result.stdout, result.stderr)
	}
	output := string(result.stdout) + string(result.stderr)
	if !strings.Contains(output, wantCause) || strings.Contains(output, "panic:") {
		t.Fatalf("missing cause %q: %s", wantCause, output)
	}
	if jsonMode {
		records := batchStatusRecords(t, result.stdout)
		terminalCount, errorCount := 0, 0
		for _, record := range records {
			switch record.Status {
			case "complete", "failed":
				terminalCount++
				if record.Status != wantStatus || !reflect.DeepEqual(record.Metric, batchStatusTestMetric(wantStatus)) {
					t.Errorf("wrong terminal result: %+v", record)
				}
			case "error":
				errorCount++
			case "pending", "in-progress":
			default:
				t.Errorf("unexpected status %q", record.Status)
			}
		}
		wantTerminal := 0
		if wantStatus != "" {
			wantTerminal = 1
		}
		if terminalCount != wantTerminal || errorCount != wantExit || len(result.stderr) != 0 {
			t.Errorf("terminal=%d errors=%d stdout=%s stderr=%s", terminalCount, errorCount, result.stdout, result.stderr)
		}
	} else {
		if strings.Contains(output, "\x1b") || strings.Contains(output, `"status"`) {
			t.Errorf("unexpected UI/JSON in text output: %q", output)
		}
		if wantStatus != "" && strings.Count(string(result.stdout), wantStatus) != 1 {
			t.Errorf("expected one %s result: %q", wantStatus, result.stdout)
		}
	}
}

func TestBatchStatusCLITerminalBufferedTail(t *testing.T) {
	for _, terminal := range []string{"complete", "failed"} {
		for _, jsonMode := range []bool{true, false} {
			t.Run(terminal+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
				server := newBatchStatusServer(t, batchStatusFixture{
					metrics: func(w http.ResponseWriter, _ *http.Request) {
						var body bytes.Buffer
						for _, status := range []string{terminal, "in-progress", terminal} {
							_ = json.NewEncoder(&body).Encode(batchStatusTestSample(status))
						}
						body.WriteString("not JSON")
						_, _ = w.Write(body.Bytes())
					},
					status: func(w http.ResponseWriter, r *http.Request) {
						t.Error("terminal live result must not fall back")
						batchStatusNoSuchJob(w, r)
					},
				})
				result := runBatchStatusCLI(t, server.URL, jsonMode)
				assertBatchStatusResult(t, result, jsonMode, 0, terminal, "")
				if jsonMode && len(batchStatusRecords(t, result.stdout)) != 1 {
					t.Errorf("output after terminal result: %s", result.stdout)
				}
			})
		}
	}
}

func TestBatchStatusCLIPersisted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		body       string
		httpCode   int
		wantExit   int
		wantCause  string
		wantStatus string
	}{
		{name: "complete", status: "complete", wantStatus: "complete"},
		{name: "failed", status: "failed", wantStatus: "failed"},
		{name: "nonterminal", status: "in-progress", wantExit: 1, wantCause: "persisted status is still in-progress"},
		{name: "missing", httpCode: http.StatusNotFound, wantExit: 1, wantCause: "missing or expired"},
		{name: "unavailable", httpCode: http.StatusForbidden, body: `{"Code":"AccessDenied","Message":"persisted status denied"}`, wantExit: 1, wantCause: "persisted status denied"},
		{name: "empty_response", body: `{}`, wantExit: 1, wantCause: "persisted status returned job ID"},
		{name: "null_metric", body: `{"LastMetric":null}`, wantExit: 1, wantCause: "persisted status returned job ID"},
		{name: "wrong_job", body: `{"LastMetric":{"jobID":"other","complete":true}}`, wantExit: 1, wantCause: "persisted status returned job ID"},
		{name: "decode_error", body: `{"LastMetric":`, wantExit: 1, wantCause: "unexpected EOF"},
	} {
		for _, jsonMode := range []bool{true, false} {
			t.Run(tc.name+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
				server := newBatchStatusServer(t, batchStatusFixture{
					describe: batchStatusNoSuchJob,
					status: func(w http.ResponseWriter, r *http.Request) {
						if tc.httpCode == http.StatusNotFound {
							batchStatusNoSuchJob(w, r)
						} else if tc.httpCode != 0 {
							w.WriteHeader(tc.httpCode)
							_, _ = io.WriteString(w, tc.body)
						} else if tc.body != "" {
							_, _ = io.WriteString(w, tc.body)
						} else {
							_ = json.NewEncoder(w).Encode(madmin.BatchJobStatus{LastMetric: batchStatusTestMetric(tc.status)})
						}
					},
				})
				result := runBatchStatusCLI(t, server.URL, jsonMode)
				assertBatchStatusResult(t, result, jsonMode, tc.wantExit, tc.wantStatus, tc.wantCause)
			})
		}
	}
}

func TestBatchStatusCLIStreamFallback(t *testing.T) {
	for _, final := range []bool{true, false} {
		for _, terminal := range []string{"complete", "failed"} {
			for _, jsonMode := range []bool{true, false} {
				t.Run("final="+strconv.FormatBool(final)+"/"+terminal+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
					server := newBatchStatusServer(t, batchStatusFixture{
						metrics: func(w http.ResponseWriter, r *http.Request) {
							sample := batchStatusTestSample("empty")
							sample.Final = final
							_ = json.NewEncoder(w).Encode(sample)
							w.(http.Flusher).Flush()
							if final {
								<-r.Context().Done()
							}
						},
						status: func(w http.ResponseWriter, _ *http.Request) {
							_ = json.NewEncoder(w).Encode(madmin.BatchJobStatus{LastMetric: batchStatusTestMetric(terminal)})
						},
					})
					result := runBatchStatusCLI(t, server.URL, jsonMode)
					assertBatchStatusResult(t, result, jsonMode, 0, terminal, "")
					if jsonMode {
						records := batchStatusRecords(t, result.stdout)
						if len(records) != 2 || records[0].Status != "pending" {
							t.Errorf("missing pending/terminal sequence: %s", result.stdout)
						}
					}
				})
			}
		}
	}
}

func TestBatchStatusCLIErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description bool
		denied      bool
		sample      string
		final       bool
		streamError bool
		transport   bool
		tail        string
		wantCause   string
	}{
		{name: "describe_denied", description: true, denied: true, wantCause: "Access Denied"},
		{name: "metrics_denied", denied: true, wantCause: "live metrics stream failed"},
		{name: "empty_stream", wantCause: "unexpected EOF"},
		{name: "empty_then_eof", sample: "empty", wantCause: "unexpected EOF"},
		{name: "ongoing_then_eof", sample: "in-progress", wantCause: "unexpected EOF"},
		{name: "malformed", tail: "not JSON", wantCause: "invalid character"},
		{name: "truncated", tail: `{"aggregated":`, wantCause: "unexpected EOF"},
		{name: "ongoing_then_malformed", sample: "in-progress", tail: "not JSON", wantCause: "invalid character"},
		{name: "stream_error", sample: "empty", streamError: true, wantCause: "metrics collection failed"},
		{name: "final_nonterminal", sample: "in-progress", final: true, wantCause: "stream ended before a terminal"},
		{name: "final_empty", sample: "empty", final: true, wantCause: "stream ended before a terminal"},
		{name: "broken_transport", transport: true, wantCause: "invalid byte in chunk length"},
	} {
		for _, jsonMode := range []bool{true, false} {
			t.Run(tc.name+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
				handler := func(w http.ResponseWriter, r *http.Request) {
					if tc.transport {
						conn, rw, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\ninvalid\r\n")
						_ = rw.Flush()
						return
					}
					if tc.denied {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"Code":"AccessDenied","Message":"Access Denied"}`)
						return
					}
					if tc.sample != "" {
						sample := batchStatusTestSample(tc.sample)
						sample.Final = tc.final
						if tc.streamError {
							sample.Errors = []string{"metrics collection failed"}
						}
						_ = json.NewEncoder(w).Encode(sample)
						w.(http.Flusher).Flush()
					}
					_, _ = io.WriteString(w, tc.tail)
					if tc.final || tc.streamError {
						<-r.Context().Done()
					}
				}
				fixture := batchStatusFixture{metrics: handler}
				if tc.description {
					fixture.describe, fixture.metrics = handler, nil
				}
				server := newBatchStatusServer(t, fixture)
				result := runBatchStatusCLI(t, server.URL, jsonMode)
				assertBatchStatusResult(t, result, jsonMode, 1, "", tc.wantCause)
			})
		}
	}
}

func TestBatchStatusMissingMetricsDeadline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sample     string
		persisted  string
		missing    bool
		wantCause  string
		wantStatus string
	}{
		{name: "queued", sample: "empty", wantCause: "exists but its status is unavailable"},
		{name: "nil", sample: "nil", wantCause: "exists but its status is unavailable"},
		{name: "other_job", sample: "other", wantCause: "exists but its status is unavailable"},
		{name: "missing", sample: "empty", missing: true, wantCause: "missing or expired"},
		{name: "persisted_complete", sample: "empty", persisted: "complete", wantStatus: "complete"},
		{name: "persisted_failed", sample: "empty", persisted: "failed", wantStatus: "failed"},
		{name: "persisted_nonterminal", sample: "empty", persisted: "in-progress", wantCause: "persisted status is still in-progress"},
		{name: "silent_stream", wantCause: "no live metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var describes atomic.Int32
			disconnected := make(chan struct{})
			server := newBatchStatusServer(t, batchStatusFixture{
				describe: func(w http.ResponseWriter, r *http.Request) {
					if describes.Add(1) > 1 && tc.missing {
						batchStatusNoSuchJob(w, r)
					} else {
						_, _ = io.WriteString(w, "version: v1\n")
					}
				},
				metrics: func(w http.ResponseWriter, r *http.Request) {
					defer close(disconnected)
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						if tc.sample != "" {
							_ = json.NewEncoder(w).Encode(batchStatusTestSample(tc.sample))
						}
						w.(http.Flusher).Flush()
						select {
						case <-ticker.C:
						case <-r.Context().Done():
							return
						}
					}
				},
				status: func(w http.ResponseWriter, r *http.Request) {
					if tc.persisted == "" {
						batchStatusNoSuchJob(w, r)
					} else {
						_ = json.NewEncoder(w).Encode(madmin.BatchJobStatus{LastMetric: batchStatusTestMetric(tc.persisted)})
					}
				},
			})
			client, err := madmin.NewWithOptions(strings.TrimPrefix(server.URL, "http://"), &madmin.Options{
				Creds: credentials.NewStaticV4("access", "secret", ""),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var records []batchJobStatusMessage
			err = watchBatchJobStatus(ctx, client, "job", 100*time.Millisecond, func(msg batchJobStatusMessage) {
				records = append(records, msg)
			})
			if tc.wantStatus != "" {
				if err != nil || len(records) != 2 || records[1].Status != tc.wantStatus {
					t.Fatalf("err=%v records=%+v", err, records)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantCause) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v, want %q before overall deadline", err, tc.wantCause)
			}
			if tc.sample != "" && (len(records) == 0 || records[0].Status != "pending") {
				t.Errorf("no explicit pending state: %+v", records)
			}
			pending := 0
			for _, record := range records {
				if record.Status == "pending" {
					pending++
				}
			}
			if pending > 1 {
				t.Errorf("repeated empty samples spammed %d pending messages", pending)
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Error("stream not canceled after deadline")
			}
		})
	}
}

func TestBatchStatusCLICancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not supported on Windows")
	}
	for _, phase := range []string{"describe", "metrics", "persisted"} {
		for _, jsonMode := range []bool{true, false} {
			t.Run(phase+"/json="+strconv.FormatBool(jsonMode), func(t *testing.T) {
				ready, disconnected := make(chan struct{}), make(chan struct{})
				block := func(w http.ResponseWriter, r *http.Request) {
					if phase == "metrics" {
						_ = json.NewEncoder(w).Encode(batchStatusTestSample("empty"))
						w.(http.Flusher).Flush()
					}
					close(ready)
					<-r.Context().Done()
					close(disconnected)
				}
				fixture := batchStatusFixture{metrics: block}
				switch phase {
				case "describe":
					fixture.describe = block
				case "persisted":
					fixture.describe, fixture.status = batchStatusNoSuchJob, block
				}
				server := newBatchStatusServer(t, fixture)
				args := []string{"batch", "status", "b4", "job"}
				if jsonMode {
					args = append(args, "--json") // Also exercise the leaf flag.
				}
				command := checksumVerifyCLICommand(t, server.URL, nil, args...)
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				t.Cleanup(func() { _ = command.Process.Kill() })
				select {
				case <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("CLI did not start request")
				}
				if err := command.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() == 0 {
						t.Fatalf("cancellation must not succeed: err=%v stdout=%s stderr=%s", err, &stdout, &stderr)
					}
					if exit.ExitCode() != globalCancelExitStatus && exit.ExitCode() != globalErrorExitStatus {
						t.Errorf("unexpected cancellation exit: %d", exit.ExitCode())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("CLI hung after interruption")
				}
				select {
				case <-disconnected:
				case <-time.After(time.Second):
					t.Fatal("request remained open after cancellation")
				}
			})
		}
	}
}

func TestBatchStatusContextCancellation(t *testing.T) {
	for _, phase := range []string{"describe", "metrics", "persisted"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			disconnected := make(chan struct{})
			block := func(_ http.ResponseWriter, r *http.Request) {
				cancel()
				<-r.Context().Done()
				close(disconnected)
			}
			fixture := batchStatusFixture{metrics: block}
			switch phase {
			case "describe":
				fixture.describe = block
			case "persisted":
				fixture.describe, fixture.status = batchStatusNoSuchJob, block
			}
			server := newBatchStatusServer(t, fixture)
			client, err := madmin.NewWithOptions(strings.TrimPrefix(server.URL, "http://"), &madmin.Options{
				Creds: credentials.NewStaticV4("access", "secret", ""),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = watchBatchJobStatus(ctx, client, "job", time.Second, func(msg batchJobStatusMessage) {
				t.Errorf("unexpected output after cancellation: %+v", msg)
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err=%v, want context cancellation", err)
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Error("request did not disconnect")
			}
		})
	}
}

func TestBatchStatusCLIRequestTimeout(t *testing.T) {
	for _, phase := range []string{"describe", "persisted"} {
		t.Run(phase, func(t *testing.T) {
			disconnected := make(chan struct{})
			block := func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
				close(disconnected)
			}
			fixture := batchStatusFixture{describe: block}
			if phase == "persisted" {
				fixture.describe, fixture.status = batchStatusNoSuchJob, block
			}
			server := newBatchStatusServer(t, fixture)
			jsonMode := phase == "describe"
			result := runBatchStatusCLI(t, server.URL, jsonMode)
			assertBatchStatusResult(t, result, jsonMode, 1, "", "context deadline exceeded")
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Error("timed-out status request remained open")
			}
		})
	}
}

func TestBatchStatusLiveMetricValidation(t *testing.T) {
	for _, jobID := range []string{"", "other"} {
		t.Run("returned="+jobID, func(t *testing.T) {
			server := newBatchStatusServer(t, batchStatusFixture{
				metrics: func(w http.ResponseWriter, r *http.Request) {
					sample := batchStatusTestSample("complete")
					job := sample.Aggregated.BatchJobs.Jobs["job"]
					job.JobID = jobID
					sample.Aggregated.BatchJobs.Jobs["job"] = job
					_ = json.NewEncoder(w).Encode(sample)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				},
			})
			result := runBatchStatusCLI(t, server.URL, true)
			assertBatchStatusResult(t, result, true, 1, "", "live metrics returned job ID")
		})
	}
}

func TestBatchStatusProgressAfterMetricGap(t *testing.T) {
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: func(w http.ResponseWriter, r *http.Request) {
			for _, status := range []string{"in-progress", "empty", "nil", "other", "in-progress", "complete"} {
				_ = json.NewEncoder(w).Encode(batchStatusTestSample(status))
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		},
	})
	result := runBatchStatusCLI(t, server.URL, true)
	assertBatchStatusResult(t, result, true, 0, "complete", "")
	var statuses []string
	for _, record := range batchStatusRecords(t, result.stdout) {
		statuses = append(statuses, record.Status)
	}
	if !reflect.DeepEqual(statuses, []string{"in-progress", "pending", "in-progress", "complete"}) {
		t.Errorf("lost state after metric gap: %v", statuses)
	}
}

func TestBatchStatusUIMissingDetails(t *testing.T) {
	for _, jobType := range []string{"", "replicate", "expire", "catalog"} {
		model := initBatchJobMetricsUI("job")
		_, _ = model.Update(batchJobStatusMessage{Status: "pending", Metric: madmin.JobMetric{JobID: "job"}})
		if view := model.View(); !strings.Contains(view, "pending") || !strings.Contains(view, "queued") {
			t.Errorf("missing pending explanation: %q", view)
		}
		_, quit := model.Update(batchStatusMessage(madmin.JobMetric{JobID: "job", JobType: jobType, Failed: true}))
		if view := model.View(); !strings.Contains(view, "failed") || quit == nil {
			t.Errorf("missing failed state or quit command: %q", view)
		}
	}
	model := initBatchJobMetricsUI("job")
	_, quit := model.Update(batchJobStatusDoneMsg{})
	if !model.quitting || quit == nil {
		t.Error("UI must stop on observer failure")
	}
	model = initBatchJobMetricsUI("job")
	_, quit = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !model.canceled || quit == nil {
		t.Error("UI must stop on user cancellation")
	}
}
