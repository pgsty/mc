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
	"strings"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
)

func TestResyncMetricsViewRates(t *testing.T) {
	// Largest counter that can be multiplied by nanoseconds per second in int64.
	const lastSafe = int64(9_223_372_036)

	for _, tc := range []struct {
		name             string
		size, count      int64
		elapsed          time.Duration
		throughput, iops string
	}{
		{
			name: "zero counters", elapsed: time.Second,
			throughput: "0 B/s", iops: "0.00 objs/s",
		},
		{
			name: "subsecond duration", size: 1024, count: 3, elapsed: 500 * time.Millisecond,
			throughput: "2.0 KiB/s", iops: "6.00 objs/s",
		},
		{
			name: "byte counter below last safe value", size: lastSafe - 1, count: 3, elapsed: time.Second,
			throughput: "8.6 GiB/s", iops: "3.00 objs/s",
		},
		{
			name: "byte counter at last safe value", size: lastSafe, count: 3, elapsed: time.Second,
			throughput: "8.6 GiB/s", iops: "3.00 objs/s",
		},
		{
			name: "byte counter at first overflow", size: lastSafe + 1, count: 3, elapsed: time.Second,
			throughput: "8.6 GiB/s", iops: "3.00 objs/s",
		},
		{
			name: "101 GiB in 95.5 seconds", size: 101 << 30, count: 191, elapsed: 95500 * time.Millisecond,
			throughput: "1.1 GiB/s", iops: "2.00 objs/s",
		},
		{
			name: "847 GiB in 13 hours 45 minutes", size: 847 << 30, count: 99_000, elapsed: 13*time.Hour + 45*time.Minute,
			throughput: "18 MiB/s", iops: "2.00 objs/s",
		},
		{
			name: "object counter below last safe value", size: 1024, count: lastSafe - 1, elapsed: time.Second,
			throughput: "1.0 KiB/s", iops: "9223372035.00 objs/s",
		},
		{
			name: "object counter at last safe value", size: 1024, count: lastSafe, elapsed: time.Second,
			throughput: "1.0 KiB/s", iops: "9223372036.00 objs/s",
		},
		{
			name: "object counter at first overflow", size: 1024, count: lastSafe + 1, elapsed: time.Second,
			throughput: "1.0 KiB/s", iops: "9223372037.00 objs/s",
		},
		{
			name: "zero duration", size: lastSafe + 1, count: lastSafe + 1,
		},
		{
			name: "negative duration", size: lastSafe + 1, count: lastSafe + 1, elapsed: -time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(1, 0).UTC()
			m := initResyncMetricsUI("peer")
			m.current = madmin.SiteResyncMetrics{
				ResyncID:        "rate-test",
				ResyncStatus:    "Ongoing",
				StartTime:       start,
				LastUpdate:      start.Add(tc.elapsed),
				ReplicatedSize:  tc.size,
				ReplicatedCount: tc.count,
			}

			// Check the displayed rows with fixed expectations, independent of
			// the rate calculation and platform-specific overflow conversions.
			rendered := reAnsi.ReplaceAllString(m.View(), "")
			rows := make(map[string]string)
			for _, line := range strings.Split(rendered, "\n") {
				if fields := strings.Fields(line); len(fields) > 1 {
					rows[fields[0]] = strings.Join(fields[1:], " ")
				}
			}
			if rows["ResyncID:"] != "rate-test" {
				t.Fatalf("missing resync status table: %s", rendered)
			}
			for label, want := range map[string]string{
				"Throughput:": tc.throughput,
				"IOPs:":       tc.iops,
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
