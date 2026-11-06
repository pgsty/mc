// Copyright (c) 2026 PGSTY
// SPDX-License-Identifier: AGPL-3.0-or-later

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestBatchStatusPausedResumesWithoutStaleDetails(t *testing.T) {
	var phase atomic.Int32
	base := batchStatusTestMetric("in-progress")
	metric := func(state int32) madmin.JobMetric {
		job := base
		job.LastUpdate = base.LastUpdate.Add(time.Duration(state) * time.Second)
		job.Complete = state == 2
		return job
	}
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: func(w http.ResponseWriter, r *http.Request) {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				sample := batchStatusTestSample("in-progress")
				sample.Aggregated.BatchJobs.Jobs["job"] = metric(phase.Load())
				_ = json.NewEncoder(w).Encode(sample)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
		},
		status: func(w http.ResponseWriter, _ *http.Request) {
			state := phase.Load()
			status := map[string]any{"jobID": "job", "lastUpdate": metric(state).LastUpdate, "complete": state == 2}
			if state == 0 {
				status["pausedReason"] = "authorization plugin unavailable"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"LastMetric": status})
		},
	})
	client, err := madmin.NewWithOptions(strings.TrimPrefix(server.URL, "http://"), &madmin.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Second)
	defer cancel()
	paused, resumed, finished := 0, false, false
	err = watchBatchJobStatus(ctx, client, "job", batchStatusIdleTimeout, func(msg batchJobStatusMessage) {
		switch msg.Status {
		case "paused":
			paused++
			if msg.Metric.Complete || msg.Metric.Failed || msg.Details == nil || msg.Details.PausedReason == "" {
				t.Errorf("pause became terminal or lost reason: %+v", msg)
			}
			// The next live sample with the same timestamp must retain the
			// pause. Only fresh progress permits clearing it.
			if paused == 2 {
				phase.Store(1)
			}
		case "in-progress":
			if paused == 1 {
				t.Error("unchanged live metric cleared the pause")
			}
			if paused >= 2 {
				resumed = true
				if msg.Details != nil && msg.Details.PausedReason != "" {
					t.Error("stale pause attached to resumed progress")
				}
				phase.Store(2)
			}
		case "complete":
			finished = true
		}
	})
	if err != nil || paused < 2 || !resumed || !finished {
		t.Fatalf("err=%v paused=%d resumed=%t finished=%t", err, paused, resumed, finished)
	}
}

func TestBatchStatusPersistedPausedAndFailedReasons(t *testing.T) {
	for _, state := range []string{"paused", "failed"} {
		t.Run(state, func(t *testing.T) {
			server := newBatchStatusServer(t, batchStatusFixture{
				describe: batchStatusNoSuchJob,
				status: func(w http.ResponseWriter, _ *http.Request) {
					field := "pausedReason"
					if state == "failed" {
						field = "failureReason"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"LastMetric": map[string]any{
						"jobID": "job", "failed": state == "failed", field: "test reason",
					}})
				},
			})
			result := runBatchStatusCLI(t, server.URL, true)
			records := batchStatusRecords(t, result.stdout)
			if len(records) == 0 || records[0].Status != state || records[0].Details == nil {
				t.Fatalf("missing %s: %s", state, result.stdout)
			}
			if state == "paused" && (result.exitCode == 0 || records[0].Metric.Failed || !strings.Contains(string(result.stdout), "persisted status is still paused")) {
				t.Fatalf("pause reported as a terminal result: %s", result.stdout)
			}
			if state == "failed" && result.exitCode != 0 {
				t.Fatalf("failed result was not retrieved: %s", result.stdout)
			}
			model := initBatchJobMetricsUI("job")
			_, quit := model.Update(records[0])
			if (quit != nil) != (state == "failed") || !strings.Contains(model.View(), "test reason") || !strings.Contains(model.View(), state) {
				t.Errorf("UI lost state or reason: %s", model.View())
			}
		})
	}
}

func TestBatchStatusTerminalRejectsStaleDetails(t *testing.T) {
	for _, body := range []string{
		`{"LastMetric":{"jobID":"other","complete":true,"keysRefused":10}}`,
		`{"LastMetric":{"jobID":"job","complete":true,"lastUpdate":"1970-01-01T00:01:41Z","keysRefused":10}}`,
		`{"LastMetric":{"jobID":"job","complete":false,"lastUpdate":"1970-01-01T00:01:42Z","pausedReason":"stale pause"}}`,
	} {
		server := newBatchStatusServer(t, batchStatusFixture{
			metrics: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(batchStatusTestSample("complete"))
			},
			status: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) },
		})
		result := runBatchStatusCLI(t, server.URL, true)
		records := batchStatusRecords(t, result.stdout)
		if result.exitCode != 0 || len(records) != 1 || records[0].Status != "complete" || records[0].Details != nil || records[0].DetailsUnavailable == "" {
			t.Errorf("stale details changed terminal result: %s", result.stdout)
		}
	}
}

func TestBatchStatusTerminalDetailsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(batchStatusTestSample("complete"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		},
		status: func(_ http.ResponseWriter, r *http.Request) {
			cancel()
			<-r.Context().Done()
		},
	})
	client, err := madmin.NewWithOptions(strings.TrimPrefix(server.URL, "http://"), &madmin.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := watchBatchJobStatus(ctx, client, "job", time.Second, func(msg batchJobStatusMessage) {
		t.Errorf("emitted after canceled details request: %+v", msg)
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}
