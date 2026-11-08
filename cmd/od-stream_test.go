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
	"strconv"
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
		{name: "size_skip_auto_parts", source: "0123456789", extra: []string{"size=2", "skip=1"}, want: "23456789", partSize: 2, parts: 4, skip: 1},
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

func TestODRejectsMalformedOperand(t *testing.T) {
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, "--json", "od", "badoperand")
	if result.exitCode != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var message struct {
		Status string       `json:"status"`
		Error  errorMessage `json:"error"`
	}
	if err := json.Unmarshal(result.stdout, &message); err != nil {
		t.Fatalf("expected JSON error: %v; stdout=%q stderr=%q", err, result.stdout, result.stderr)
	}
	if message.Status != "error" || message.Error.Type != "fatal" ||
		!strings.Contains(message.Error.Message, "Invalid od operand") ||
		!strings.Contains(message.Error.Cause.Message, "Invalid arguments provided") {
		t.Fatalf("unexpected malformed operand error: %+v", message)
	}
	if len(result.stderr) != 0 {
		t.Fatalf("unexpected stderr: %q", result.stderr)
	}
}

func TestODRejectsInvalidNumericArguments(t *testing.T) {
	partsOverflow, skipOverflow := "parts and size exceed", "skip and size exceed"
	if strconv.IntSize == 32 {
		// These counts are rejected by Atoi before transfer-size arithmetic.
		partsOverflow, skipOverflow = "value out of range", "value out of range"
	}
	for _, tc := range []struct {
		name, extra, wantError string
	}{
		{name: "negative_parts", extra: "parts=-1", wantError: "parts must be non-negative"},
		{name: "negative_skip", extra: "skip=-1", wantError: "skip must be non-negative"},
		{name: "parts_overflow", extra: "parts=9223372036854775807", wantError: partsOverflow},
		{name: "skip_overflow", extra: "skip=9223372036854775807", wantError: skipOverflow},
		{name: "skip_beyond_source", extra: "skip=6", wantError: "skip exceeds source size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.WriteFile(source, []byte("0123456789"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			args := []string{"--json", "od", "if=" + source, "of=" + target, "size=2", tc.extra}
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
			if result.exitCode != 1 {
				t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			var message struct {
				Status string       `json:"status"`
				Error  errorMessage `json:"error"`
			}
			if err := json.Unmarshal(result.stdout, &message); err != nil {
				t.Fatalf("expected JSON error: %v; stdout=%q stderr=%q", err, result.stdout, result.stderr)
			}
			if message.Status != "error" || !strings.Contains(message.Error.Cause.Message, tc.wantError) {
				t.Fatalf("error cause=%q, want %q", message.Error.Cause.Message, tc.wantError)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "keep" {
				t.Fatalf("target=%q, error=%v, want keep", got, err)
			}
		})
	}
}
