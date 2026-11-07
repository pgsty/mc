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
	"testing"

	"github.com/minio/madmin-go/v3"
)

func TestTopDriveFirstSampleBaseline(t *testing.T) {
	disk := madmin.Disk{Endpoint: "disk", TotalSpace: 1 << 40, UsedSpace: 1 << 39}
	for _, tc := range []struct {
		name  string
		first madmin.DiskIOStats
	}{
		{name: "zero_counters"},
		{name: "lifetime_counters", first: madmin.DiskIOStats{
			ReadIOs: 1000, ReadSectors: 204800, ReadTicks: 5000, TotalTicks: 10000,
			WriteIOs: 500, WriteSectors: 102400, WriteTicks: 3000,
			DiscardIOs: 100, DiscardSectors: 20480, DiscardTicks: 2000,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := initTopDriveUI([]madmin.Disk{disk}, 10)
			model.Update(topDriveResult{diskName: disk.Endpoint, stats: tc.first})
			initial := generateDriveStat(disk, model.currTopMap[disk.Endpoint], model.prevTopMap[disk.Endpoint], 1000)
			wantIdle := driveIOStat{endpoint: disk.Endpoint, used: 50}
			if initial != wantIdle {
				t.Fatalf("first sample must retain capacity without reporting lifetime I/O: %+v", initial)
			}

			second := tc.first
			second.ReadIOs += 10
			second.ReadSectors += 2048
			second.ReadTicks += 50
			second.TotalTicks += 50
			model.Update(topDriveResult{diskName: disk.Endpoint, stats: second})
			got := generateDriveStat(disk, model.currTopMap[disk.Endpoint], model.prevTopMap[disk.Endpoint], 1000)
			want := driveIOStat{endpoint: disk.Endpoint, used: 50, readMBs: 1, tps: 10, util: 5, await: 5}
			if got != want {
				t.Fatalf("second sample=%+v, want %+v", got, want)
			}

			model.Update(topDriveResult{diskName: disk.Endpoint, stats: second})
			idle := generateDriveStat(disk, model.currTopMap[disk.Endpoint], model.prevTopMap[disk.Endpoint], 1000)
			if idle != wantIdle {
				t.Fatalf("idle sample=%+v, want %+v", idle, wantIdle)
			}
		})
	}
}
