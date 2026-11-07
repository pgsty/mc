// Copyright (c) 2026 PGSTY
// SPDX-License-Identifier: AGPL-3.0-or-later

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// The server publishes live metrics from a copy it saves before it stamps and
// persists the job, so a live sample's lastUpdate is never later than the
// saved status and usually lags it. These tests model that ordering, which the
// other fixtures leave out by giving both sources the same timestamp.

func batchStatusLiveClient(t *testing.T, url string) *madmin.AdminClient {
	t.Helper()
	client, err := madmin.NewWithOptions(strings.TrimPrefix(url, "http://"), &madmin.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// batchStatusLiveStream pushes metric() every 50ms until the client leaves.
func batchStatusLiveStream(metric func() madmin.JobMetric) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			sample := batchStatusTestSample("in-progress")
			sample.Aggregated.BatchJobs.Jobs["job"] = metric()
			_ = json.NewEncoder(w).Encode(sample)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}
}

func batchStatusPersisted(metric madmin.JobMetric, extra map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(metric)
		fields := make(map[string]any)
		_ = json.Unmarshal(body, &fields)
		for k, v := range extra {
			fields[k] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"LastMetric": fields})
	}
}

// A healthy job whose saved status is newer than its live samples keeps
// streaming after the status poll; the poll must not start the idle timeout.
func TestBatchStatusLiveSamplesContinueAfterNewerPersistedPoll(t *testing.T) {
	base := batchStatusTestMetric("in-progress")
	var objects atomic.Int64
	live := func() madmin.JobMetric {
		m := base
		m.Replicate = &madmin.ReplicateInfo{Objects: objects.Add(1)}
		return m
	}
	persisted := base
	persisted.LastUpdate = base.LastUpdate.Add(time.Second)
	persisted.Replicate = &madmin.ReplicateInfo{Objects: 1}
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: batchStatusLiveStream(live),
		status:  batchStatusPersisted(persisted, nil),
	})
	ctx, cancel := context.WithTimeout(t.Context(), batchStatusDetailsPollInterval+3*time.Second)
	defer cancel()
	start := time.Now()
	var afterPoll int
	var last int64
	err := watchBatchJobStatus(ctx, batchStatusLiveClient(t, server.URL), "job", 2*time.Second, func(msg batchJobStatusMessage) {
		if msg.Status != "in-progress" || msg.Metric.Replicate == nil {
			t.Errorf("unexpected message: %+v", msg)
			return
		}
		if msg.Metric.Replicate.Objects < last {
			t.Errorf("progress went backwards: %d after %d", msg.Metric.Replicate.Objects, last)
		}
		last = msg.Metric.Replicate.Objects
		if time.Since(start) > batchStatusDetailsPollInterval+time.Second {
			afterPoll++
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a healthy job ended the observation: %v", err)
	}
	if afterPoll < 10 {
		t.Fatalf("live samples stopped after the status poll: %d messages after it", afterPoll)
	}
}

// A paused job with a newer saved status stays observable as paused after the
// poll instead of being reported as having no live metrics.
func TestBatchStatusPausedRemainsObservedAfterPoll(t *testing.T) {
	base := batchStatusTestMetric("in-progress")
	persisted := base
	persisted.LastUpdate = base.LastUpdate.Add(time.Second)
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: batchStatusLiveStream(func() madmin.JobMetric { return base }),
		status:  batchStatusPersisted(persisted, map[string]any{"pausedReason": "authorization plugin unavailable"}),
	})
	ctx, cancel := context.WithTimeout(t.Context(), batchStatusDetailsPollInterval+3*time.Second)
	defer cancel()
	start := time.Now()
	var pausedLate int
	err := watchBatchJobStatus(ctx, batchStatusLiveClient(t, server.URL), "job", 2*time.Second, func(msg batchJobStatusMessage) {
		if msg.Status == "paused" && time.Since(start) > batchStatusDetailsPollInterval+time.Second {
			pausedLate++
			if msg.Details == nil || msg.Details.PausedReason == "" || msg.Metric.Complete || msg.Metric.Failed {
				t.Errorf("pause lost its reason or became terminal: %+v", msg)
			}
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a paused job ended the observation: %v", err)
	}
	if pausedLate < 10 {
		t.Fatalf("paused status stopped after the status poll: %d messages after it", pausedLate)
	}
}

// A completion that the status poll sees before the live stream does is
// reported at once, also without details, not after the idle timeout.
func TestBatchStatusPollReportsCompletionBeforeLiveStream(t *testing.T) {
	base := batchStatusTestMetric("in-progress")
	done := base
	done.Complete = true
	done.LastUpdate = base.LastUpdate.Add(time.Second)
	start := time.Now()
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: batchStatusLiveStream(func() madmin.JobMetric { return base }),
		status: func(w http.ResponseWriter, r *http.Request) {
			if time.Since(start) < batchStatusDetailsPollInterval-time.Second {
				batchStatusPersisted(base, nil)(w, r)
				return
			}
			batchStatusPersisted(done, nil)(w, r)
		},
	})
	ctx, cancel := context.WithTimeout(t.Context(), batchStatusDetailsPollInterval+10*time.Second)
	defer cancel()
	var completed time.Duration
	err := watchBatchJobStatus(ctx, batchStatusLiveClient(t, server.URL), "job", batchStatusIdleTimeout, func(msg batchJobStatusMessage) {
		if msg.Status == "complete" && completed == 0 {
			completed = time.Since(start)
		}
	})
	if err != nil {
		t.Fatalf("completion was not returned: %v", err)
	}
	if completed == 0 || completed > batchStatusDetailsPollInterval+2*time.Second {
		t.Fatalf("completion reported after %s, want right after the first poll", completed)
	}
}

// The live sample has the latest counters; a saved snapshot of a running job
// must not freeze or roll back the progress it reports.
func TestBatchStatusPersistedSnapshotKeepsLiveProgress(t *testing.T) {
	base := batchStatusTestMetric("in-progress")
	var objects atomic.Int64
	objects.Store(100)
	live := func() madmin.JobMetric {
		m := base
		m.Replicate = &madmin.ReplicateInfo{Objects: objects.Add(1)}
		return m
	}
	persisted := base
	persisted.Replicate = &madmin.ReplicateInfo{Objects: 7}
	server := newBatchStatusServer(t, batchStatusFixture{
		metrics: batchStatusLiveStream(live),
		status:  batchStatusPersisted(persisted, nil),
	})
	ctx, cancel := context.WithTimeout(t.Context(), batchStatusDetailsPollInterval+2*time.Second)
	defer cancel()
	start := time.Now()
	var last, lastAfterPoll int64
	err := watchBatchJobStatus(ctx, batchStatusLiveClient(t, server.URL), "job", batchStatusIdleTimeout, func(msg batchJobStatusMessage) {
		if msg.Metric.Replicate == nil {
			t.Errorf("unexpected message: %+v", msg)
			return
		}
		got := msg.Metric.Replicate.Objects
		if got < last {
			t.Errorf("progress went backwards: %d after %d", got, last)
		}
		last = got
		if time.Since(start) > batchStatusDetailsPollInterval+500*time.Millisecond {
			lastAfterPoll = got
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("observation ended: %v", err)
	}
	if lastAfterPoll <= 100 {
		t.Fatalf("progress froze at the saved counters after the poll: %d", lastAfterPoll)
	}
}

func TestBatchStatusSnapshotChoosesNewestMetric(t *testing.T) {
	live := batchStatusTestMetric("in-progress")
	live.Replicate = &madmin.ReplicateInfo{Objects: 20}
	older := live
	older.Replicate = &madmin.ReplicateInfo{Objects: 5}
	newer := older
	newer.LastUpdate = live.LastUpdate.Add(time.Second)
	newer.Replicate = &madmin.ReplicateInfo{Objects: 25}
	done := newer
	done.Complete = true
	paused := &batchStatusDetails{PausedReason: "paused"}

	msg, valid := batchStatusSnapshotMessage(live, older, paused)
	if !valid || msg.Metric.Replicate.Objects != 20 || msg.Status != "paused" {
		t.Fatalf("a running job must keep the live counters with the saved details: %+v valid=%t", msg, valid)
	}
	msg, valid = batchStatusSnapshotMessage(live, newer, paused)
	if !valid || msg.Metric.Replicate.Objects != 20 || msg.Status != "paused" {
		t.Fatalf("a newer saved snapshot of a running job must not replace the live counters: %+v valid=%t", msg, valid)
	}
	msg, valid = batchStatusSnapshotMessage(live, done, nil)
	if !valid || !msg.Metric.Complete || msg.Status != "complete" {
		t.Fatalf("a saved terminal snapshot must be reported as saved: %+v valid=%t", msg, valid)
	}
	msg, valid = batchStatusSnapshotMessage(madmin.JobMetric{}, older, nil)
	if !valid || msg.Metric.Replicate.Objects != 5 {
		t.Fatalf("without a live sample the saved snapshot stands: %+v valid=%t", msg, valid)
	}
	if got := newestBatchMetric(live, older); got.Replicate.Objects != 20 {
		t.Fatalf("a tie must keep the first metric: %+v", got)
	}
	if got := newestBatchMetric(live, newer); got.Replicate.Objects != 25 {
		t.Fatalf("the later timestamp must win: %+v", got)
	}
}
