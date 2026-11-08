package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/madmin-go/v3"
)

type batchRealtimeMetrics struct {
	madmin.RealtimeMetrics
	Aggregated struct {
		BatchJobs *batchLiveJobs `json:"batchJobs,omitempty"`
	} `json:"aggregated"`
}

type batchLiveJobs struct {
	Jobs map[string]batchStatusMetric
}

func (metric *batchStatusMetric) UnmarshalJSON(data []byte) error {
	type plainMetric batchStatusMetric
	var decoded plainMetric
	wire := struct {
		*plainMetric
		VersionsSkippedLocked *int64 `json:"versionsSkippedLocked"`
	}{plainMetric: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*metric = batchStatusMetric(decoded)
	if wire.VersionsSkippedLocked != nil {
		metric.VersionsSkippedLocked = *wire.VersionsSkippedLocked
	}
	metric.detailsPresent = wire.VersionsSkippedLocked != nil || !metric.empty()
	return nil
}

func readBatchLiveMetrics(ctx context.Context, client batchStatusClient, jobID string, emit func(batchRealtimeMetrics)) error {
	requestClient, ok := client.(batchStatusDetailsClient)
	if !ok {
		return client.Metrics(ctx, madmin.MetricsOptions{
			Type: madmin.MetricsBatchJobs, ByJobID: jobID, Interval: time.Second,
		}, func(sample madmin.RealtimeMetrics) {
			out := batchRealtimeMetrics{RealtimeMetrics: sample}
			if sample.Aggregated.BatchJobs != nil {
				out.Aggregated.BatchJobs = &batchLiveJobs{Jobs: make(map[string]batchStatusMetric)}
				for id, job := range sample.Aggregated.BatchJobs.Jobs {
					out.Aggregated.BatchJobs.Jobs[id] = batchStatusMetric{JobMetric: job}
				}
			}
			emit(out)
		})
	}
	response, err := requestClient.ExecuteMethod(ctx, http.MethodGet, madmin.RequestData{
		RelPath: "/v3/metrics",
		QueryValues: url.Values{
			"types":    {strconv.FormatUint(uint64(madmin.MetricsBatchJobs), 10)},
			"n":        {"0"},
			"interval": {time.Second.String()},
			"by-jobID": {jobID},
		},
	})
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return errors.New("empty batch metrics response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("batch metrics returned HTTP %s", response.Status)
	}
	decoder := json.NewDecoder(response.Body)
	for {
		var sample batchRealtimeMetrics
		if err := decoder.Decode(&sample); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		emit(sample)
		if sample.Final {
			return nil
		}
	}
}
