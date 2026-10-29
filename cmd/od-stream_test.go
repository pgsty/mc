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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestODCopySizesCLI(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, want string
		extra                      []string
		wantExit                   int
		partSize                   uint64
		parts, skip                int
	}{
		{name: "empty_default", target: "old", parts: 1},
		{name: "empty_explicit_parts", target: "old", extra: []string{"parts=3"}, parts: 3},
		{name: "explicit_zero", source: "data", target: "keep", extra: []string{"size=0"}, want: "keep", wantExit: 1},
		{name: "explicit_zero_bytes", source: "data", target: "keep", extra: []string{"size=0B"}, want: "keep", wantExit: 1},
		{name: "explicit_zero_empty_source", target: "keep", extra: []string{"size=0"}, want: "keep", wantExit: 1},
		{name: "normal", source: "0123456789", target: "old", want: "0123456789", partSize: 10, parts: 1},
		{name: "parts_skip", source: "0123456789", extra: []string{"size=2", "parts=2", "skip=1"}, want: "2345", partSize: 2, parts: 2, skip: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.WriteFile(source, []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(tc.target), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--json", "od", "if=" + source, "of=" + target}, tc.extra...)
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
			if result.exitCode != tc.wantExit {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", result.exitCode, tc.wantExit, result.stdout, result.stderr)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != tc.want {
				t.Fatalf("target=%q, error=%v, want %q", data, err, tc.want)
			}
			if tc.wantExit != 0 {
				if !strings.Contains(string(result.stdout)+string(result.stderr), "size must be greater than zero") {
					t.Fatalf("missing size diagnostic: stdout=%s stderr=%s", result.stdout, result.stderr)
				}
				return
			}
			var message odMessage
			if err := json.Unmarshal(result.stdout, &message); err != nil {
				t.Fatal(err)
			}
			if message.Status != "success" || message.TotalSize != int64(len(tc.want)) ||
				message.PartSize != tc.partSize || message.Parts != tc.parts || message.Skip != tc.skip {
				t.Errorf("unexpected copy summary: %+v", message)
			}
		})
	}
}
