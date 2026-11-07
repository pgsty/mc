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
	"fmt"
	"math"
	"testing"
	"time"
)

func TestHealProgressSizeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1024, "1024 B"},
		{1025, "1 KiB"},
		{1 << 20, "1024 KiB"},
		{1 << 60, "1024 PiB"},
		{2 << 60, "2 EiB"},
		{math.MaxInt64, "8 EiB"},
	} {
		t.Run(fmt.Sprint(tc.bytes), func(t *testing.T) {
			ui := uiData{BytesScanned: tc.bytes, ObjectsScanned: 1234, HealDuration: 1500 * time.Millisecond}
			count, size, duration := ui.getProgress()
			if size != tc.want {
				t.Errorf("size=%q, want %q", size, tc.want)
			}
			if count != "1,234" || duration != "2s" {
				t.Errorf("count/duration changed: %q %q", count, duration)
			}
		})
	}
}
