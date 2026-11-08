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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMoveRejectsNormalizedSelfPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "object")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil,
		"--json", "mv", source, dir+string(filepath.Separator)+"."+string(filepath.Separator)+"object")
	if result.exitCode == 0 {
		t.Fatalf("self move succeeded: stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "keep" {
		t.Fatalf("self move changed source: data=%q error=%v", got, err)
	}
}

func TestMoveRejectsExpandedSelfDestinations(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
			for _, file := range []string{a, b} {
				if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--json", "mv", a, b, dir + string(filepath.Separator)}
			if kind != "directory" {
				link := filepath.Join(dir, "link")
				linkFn := os.Symlink
				if kind == "hardlink" {
					linkFn = os.Link
				}
				if err := linkFn(a, link); err != nil {
					t.Skipf("link unavailable: %v", err)
				}
				args = []string{"--json", "mv", a, link}
			}
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
			if result.exitCode != 1 || !strings.Contains(string(result.stdout), "itself") {
				t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			for _, file := range []string{a, b} {
				if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
					t.Fatalf("self move changed %s: data=%q error=%v", file, data, err)
				}
			}
		})
	}
}

func TestSameCopyObjectNormalizesResolvedPaths(t *testing.T) {
	urls := URLs{
		SourceContent: &ClientContent{URL: ClientURL{Type: fileSystem, Path: "/tmp/move/object"}},
		TargetContent: &ClientContent{URL: ClientURL{Type: fileSystem, Path: "/tmp/move/./object"}},
	}
	if !sameCopyObject(urls) {
		t.Fatal("equivalent source and destination paths were not detected")
	}
}
