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
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
)

func TestBatchReplicateMetricsViewRates(t *testing.T) {
	for _, tc := range []struct {
		objects           int64
		oneSecond, oneDay string
	}{
		{0, "0.00 objs/s", "0.00 objs/s"},
		{9_223_372_035, "9223372035.00 objs/s", "106751.99 objs/s"},
		{9_223_372_036, "9223372036.00 objs/s", "106751.99 objs/s"},
		{9_223_372_037, "9223372037.00 objs/s", "106751.99 objs/s"},
		// The existing float64 display rounds MaxInt64 to 2^63.
		{math.MaxInt64, "9223372036854775808.00 objs/s", "106751991167300.64 objs/s"},
	} {
		for _, period := range []struct {
			elapsed          time.Duration
			iops, throughput string
		}{
			{time.Second, tc.oneSecond, "84 MiB/s"},
			{24 * time.Hour, tc.oneDay, "1.0 KiB/s"},
			{0, "", ""},
			{-time.Second, "", ""},
		} {
			t.Run(fmt.Sprintf("%d/%s", tc.objects, period.elapsed), func(t *testing.T) {
				start := time.Unix(1, 0).UTC()
				m := initBatchJobMetricsUI("rate-test")
				m.metric = madmin.JobMetric{
					JobType:    string(madmin.BatchJobReplicate),
					StartTime:  start,
					LastUpdate: start.Add(period.elapsed),
					Replicate: &madmin.ReplicateInfo{
						Objects:          tc.objects,
						BytesTransferred: 24 * 60 * 60 * 1024,
					},
				}

				rendered := reAnsi.ReplaceAllString(m.View(), "")
				if strings.Contains(rendered, "NaN") || strings.Contains(rendered, "Inf") {
					t.Errorf("non-finite value in batch status: %s", rendered)
				}

				// Match whole fields, including signs, against fixed expectations.
				rows := make(map[string]string)
				for _, line := range strings.Split(rendered, "\n") {
					if fields := strings.Fields(line); len(fields) > 1 {
						rows[fields[0]] = strings.Join(fields[1:], " ")
					}
				}
				if rows["JobType:"] != "replicate" {
					t.Fatalf("missing batch replicate status table: %s", rendered)
				}
				for label, want := range map[string]string{
					"IOPs:":        period.iops,
					"Throughput:":  period.throughput,
					"Transferred:": "84 MiB",
				} {
					got, present := rows[label]
					if want == "" {
						if present {
							t.Errorf("%s must be absent for nonpositive elapsed time, got %q", label, got)
						}
					} else if !present || got != want {
						t.Errorf("%s got %q, want %q", label, got, want)
					}
				}
			})
		}
	}
}
