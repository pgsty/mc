// Copyright (c) 2026 Chen Shi
//
// This file is part of PGSTY SILO mcli
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	checkv1 "gopkg.in/check.v1"
)

func (s *TestSuite) TestMirrorListsEmptyDirectoriesAndFiles(c *checkv1.C) {
	sourceRoot, err := os.MkdirTemp(os.TempDir(), "mirror-source-")
	c.Assert(err, checkv1.IsNil)
	defer os.RemoveAll(sourceRoot)

	targetRoot, err := os.MkdirTemp(os.TempDir(), "mirror-target-")
	c.Assert(err, checkv1.IsNil)
	defer os.RemoveAll(targetRoot)

	emptyDir := filepath.Join(sourceRoot, "empty")
	nestedEmptyDir := filepath.Join(sourceRoot, "parent", "nested-empty")
	nonEmptyDir := filepath.Join(sourceRoot, "non-empty")
	for _, dir := range []string{emptyDir, nestedEmptyDir, nonEmptyDir} {
		c.Assert(os.MkdirAll(dir, 0o755), checkv1.IsNil)
	}
	for _, file := range []string{
		filepath.Join(sourceRoot, "empty-file"),
		filepath.Join(nonEmptyDir, "nested-empty-file"),
	} {
		c.Assert(os.WriteFile(file, nil, 0o644), checkv1.IsNil)
	}
	targetOnlyDir := filepath.Join(targetRoot, "target-only-empty")
	c.Assert(os.MkdirAll(targetOnlyDir, 0o755), checkv1.IsNil)

	source, fsErr := fsNew(sourceRoot + string(filepath.Separator))
	c.Assert(fsErr, checkv1.IsNil)
	target, fsErr := fsNew(targetRoot + string(filepath.Separator))
	c.Assert(fsErr, checkv1.IsNil)

	directories := make(map[string]bool)
	files := make(map[string]int64)
	for content := range source.List(globalContext, ListOptions{
		Recursive:        true,
		ShowDir:          DirNone,
		IncludeEmptyDirs: true,
	}) {
		c.Assert(content.Err, checkv1.IsNil)
		if content.Type.IsDir() {
			directories[content.URL.Path] = true
			continue
		}
		if content.Type.IsRegular() {
			files[filepath.Clean(content.URL.Path)] = content.Size
		}
	}
	c.Assert(directories, checkv1.DeepEquals, map[string]bool{
		emptyDir + string(filepath.Separator):       true,
		nestedEmptyDir + string(filepath.Separator): true,
	})
	c.Assert(files, checkv1.DeepEquals, map[string]int64{
		filepath.Join(sourceRoot, "empty-file"):         0,
		filepath.Join(nonEmptyDir, "nested-empty-file"): 0,
	})

	first := make(map[string]bool)
	second := make(map[string]bool)
	for diff := range objectDifference(context.Background(), source, target, mirrorOptions{includeEmptyDirs: true}) {
		c.Assert(diff.Error, checkv1.IsNil)
		switch diff.Diff {
		case differInFirst:
			first[diff.FirstURL] = true
		case differInSecond:
			second[diff.SecondURL] = true
		default:
			c.Fatalf("unexpected mirror difference: %v", diff.Diff)
		}
	}
	c.Assert(first, checkv1.DeepEquals, map[string]bool{
		emptyDir + string(filepath.Separator):           true,
		nestedEmptyDir + string(filepath.Separator):     true,
		filepath.Join(sourceRoot, "empty-file"):         true,
		filepath.Join(nonEmptyDir, "nested-empty-file"): true,
	})
	c.Assert(second, checkv1.DeepEquals, map[string]bool{
		targetOnlyDir + string(filepath.Separator): true,
	})
}

func TestMirrorRemoveWithDirectorySymlink(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "a")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	for _, dir := range []string{source, target} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(target, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := runChecksumVerifyCLI(t, "http://localhost", nil, "--json", "mirror", "--remove", "--max-workers", "1", source, target)
	if result.exitCode != 0 || bytes.Contains(result.stdout, []byte("a.txt")) {
		t.Fatalf("mirror must leave the matching file alone: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	data, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil || string(data) != "same" {
		t.Fatalf("a.txt after mirror: %q, %v", data, err)
	}
}

func TestDiffIgnoresLocalEmptyDirectories(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := runChecksumVerifyCLI(t, "http://localhost", nil, "--json", "diff", source, target)
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stdout)) != 0 {
		t.Fatalf("diff exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func TestMirrorDirectoryKeyOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "a.b", "a0"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := fsNew(filepath.ToSlash(root) + "/")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for entry := range client.List(t.Context(), ListOptions{Recursive: true, IncludeEmptyDirs: true}) {
		if entry.Err != nil {
			t.Fatal(entry.Err)
		}
		keys = append(keys, filepath.ToSlash(strings.TrimPrefix(entry.URL.Path, client.GetURL().Path)))
	}
	if want := []string{"a.b/", "a.txt", "a/", "a0/"}; !slices.Equal(keys, want) {
		t.Fatalf("keys in merge order: got %v, want %v", keys, want)
	}
}

func TestMirrorRemoveKeepsParentOfIncomingFile(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	for _, root := range []string{source, target} {
		if err := os.Mkdir(filepath.Join(root, "parent"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "parent", "child"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runChecksumVerifyCLI(t, "http://localhost", nil, "--json", "mirror", "--remove", "--max-workers", "1", source, target)
	if result.exitCode != 0 || bytes.Contains(result.stdout, []byte(`"source":""`)) {
		t.Fatalf("unexpected removal: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	if data, err := os.ReadFile(filepath.Join(target, "parent", "child")); err != nil || string(data) != "data" {
		t.Fatalf("child after mirror: %q, %v", data, err)
	}
}
