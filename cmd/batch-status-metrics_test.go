package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBatchStatusCLILiveDetails(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		for _, state := range []string{"complete", "complete-with-skips", "failed", "paused"} {
			t.Run(fmt.Sprintf("%s/json=%t", state, jsonMode), func(t *testing.T) {
				var savedRequests atomic.Int32
				server := newBatchStatusServer(t, batchStatusFixture{
					metrics: func(writer http.ResponseWriter, request *http.Request) {
						job := batchStatusTestMetric(state)
						job.Complete = strings.HasPrefix(state, "complete")
						metric := batchStatusMetric{JobMetric: job}
						switch state {
						case "complete-with-skips":
							metric.VersionsSkippedLocked, metric.KeysRefused = 2, 1
						case "failed":
							metric.FailureReason = "authorization denied"
						case "paused":
							metric.PausedReason = "authorization plugin unavailable"
						}
						emit := func(final bool) {
							data, err := json.Marshal(metric)
							if err != nil {
								t.Error(err)
								return
							}
							fields := make(map[string]any)
							if err := json.Unmarshal(data, &fields); err != nil {
								t.Error(err)
								return
							}
							fields["versionsSkippedLocked"] = metric.VersionsSkippedLocked
							sample := map[string]any{
								"aggregated": map[string]any{"batchJobs": map[string]any{"Jobs": map[string]any{"job": fields}}},
								"final":      final,
							}
							if err := json.NewEncoder(writer).Encode(sample); err != nil {
								t.Error(err)
							}
							writer.(http.Flusher).Flush()
						}
						emit(state != "paused")
						if state == "paused" {
							metric.PausedReason = ""
							metric.Complete = true
							emit(true)
						}
						<-request.Context().Done()
					},
					status: func(writer http.ResponseWriter, _ *http.Request) {
						savedRequests.Add(1)
						http.Error(writer, "saved status unavailable", http.StatusServiceUnavailable)
					},
				})
				result := runBatchStatusCLI(t, server.URL, jsonMode)
				if result.exitCode != 0 || savedRequests.Load() != 0 {
					t.Fatalf("live details depended on saved status: exit=%d requests=%d stdout=%s stderr=%s",
						result.exitCode, savedRequests.Load(), result.stdout, result.stderr)
				}
				if jsonMode {
					records := batchStatusRecords(t, result.stdout)
					if len(records) == 0 || records[0].Status != state || records[0].DetailsUnavailable != "" {
						t.Fatalf("lost live status: %s", result.stdout)
					}
					switch state {
					case "complete-with-skips":
						if records[0].Details == nil || records[0].Details.KeysRefused != 1 || records[0].Details.VersionsSkippedLocked != 2 {
							t.Fatalf("lost live counters: %s", result.stdout)
						}
					case "failed":
						if records[0].Details == nil || records[0].Details.FailureReason != "authorization denied" {
							t.Fatalf("lost live refusal: %s", result.stdout)
						}
					case "paused":
						if len(records) != 2 || records[0].Metric.Complete || records[0].Metric.Failed ||
							records[0].Details == nil || records[0].Details.PausedReason == "" ||
							records[1].Status != "complete" || records[1].Details != nil {
							t.Fatalf("pause was terminal or leaked into completion: %s", result.stdout)
						}
					}
				} else {
					for _, want := range map[string][]string{
						"complete":            {"complete"},
						"complete-with-skips": {"complete-with-skips", "VersionsSkippedLocked", "KeysRefused"},
						"failed":              {"failed", "authorization denied"},
						"paused":              {"paused", "authorization plugin unavailable", "complete"},
					}[state] {
						if !strings.Contains(string(result.stdout), want) {
							t.Errorf("missing %q in terminal output: %s", want, result.stdout)
						}
					}
				}
			})
		}
	}
}

func TestBatchStatusLiveDetailsPresence(t *testing.T) {
	for _, testCase := range []struct {
		body    string
		present bool
	}{
		{`{"jobID":"job","complete":true}`, false},
		{`{"jobID":"job","complete":true,"versionsSkippedLocked":0}`, true},
		{`{"jobID":"job","complete":true,"keysRefused":1}`, true},
		{`{"jobID":"job","pausedReason":"unavailable"}`, true},
	} {
		var metric batchStatusMetric
		if err := json.Unmarshal([]byte(testCase.body), &metric); err != nil {
			t.Fatal(err)
		}
		if metric.detailsPresent != testCase.present || metric.JobID != "job" {
			t.Fatalf("details presence for %s: %+v", testCase.body, metric)
		}
	}
}
