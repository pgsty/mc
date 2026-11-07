// Copyright (c) 2026 PGSTY
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

func TestSiteSpeedRates(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{
		{500 * time.Millisecond, "200 MiB/s"},
		{1500 * time.Millisecond, "67 MiB/s"},
		{time.Second, "100 MiB/s"},
		{10 * time.Second, "10 MiB/s"},
		{0, ""},
		{-time.Second, ""},
	} {
		t.Run(tc.duration.String(), func(t *testing.T) {
			model := initSpeedTestUI()
			model.result = PerfTestResult{
				Type:  SiteReplicationPerfTest,
				Final: true,
				SiteReplicationResult: &madmin.SiteNetPerfResult{
					NodeResults: []madmin.SiteNetPerfNodeResult{{
						Endpoint: "site", RX: 100 << 20, TX: 100 << 20,
						RXTotalDuration: tc.duration, TXTotalDuration: tc.duration,
					}},
				},
			}
			view := model.View()
			if tc.want == "" {
				if !strings.Contains(view, "RXTotalDuration") || !strings.Contains(view, "TXTotalDuration") {
					t.Fatalf("missing duration diagnostic: %s", view)
				}
			} else if strings.Count(view, tc.want) != 2 {
				t.Fatalf("want RX and TX %q: %s", tc.want, view)
			}
		})
	}
}

func TestSiteSpeedUsesEachDirectionDuration(t *testing.T) {
	model := initSpeedTestUI()
	model.result.SiteReplicationResult = &madmin.SiteNetPerfResult{
		NodeResults: []madmin.SiteNetPerfNodeResult{{
			Endpoint: "site", RX: 100 << 20, TX: 100 << 20,
			RXTotalDuration: 500 * time.Millisecond,
			TXTotalDuration: 1500 * time.Millisecond,
		}},
	}
	view := model.View()
	if !strings.Contains(view, "200 MiB/s") || !strings.Contains(view, "67 MiB/s") {
		t.Fatalf("RX and TX must use their respective durations: %s", view)
	}
}

func TestNetSpeedRetainsReportedRates(t *testing.T) {
	model := initSpeedTestUI()
	model.result = PerfTestResult{
		Type: NetPerfTest,
		NetResult: &madmin.NetperfResult{
			NodeResults: []madmin.NetperfNodeResult{{
				Endpoint: "node", RX: 100 << 20, TX: 200 << 20,
			}},
		},
	}
	view := model.View()
	if !strings.Contains(view, "100 MiB/s") || !strings.Contains(view, "200 MiB/s") {
		t.Fatalf("regular netperf already reports bytes per second: %s", view)
	}
}
