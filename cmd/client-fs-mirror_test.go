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
	"context"
	"os"
	"path/filepath"

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
			directories[filepath.Clean(content.URL.Path)] = true
			continue
		}
		if content.Type.IsRegular() {
			files[filepath.Clean(content.URL.Path)] = content.Size
		}
	}
	c.Assert(directories, checkv1.DeepEquals, map[string]bool{
		filepath.Clean(emptyDir):       true,
		filepath.Clean(nestedEmptyDir): true,
	})
	c.Assert(files, checkv1.DeepEquals, map[string]int64{
		filepath.Join(sourceRoot, "empty-file"):         0,
		filepath.Join(nonEmptyDir, "nested-empty-file"): 0,
	})

	first := make(map[string]bool)
	second := make(map[string]bool)
	for diff := range objectDifference(context.Background(), source, target, mirrorOptions{}) {
		c.Assert(diff.Error, checkv1.IsNil)
		switch diff.Diff {
		case differInFirst:
			first[filepath.Clean(diff.FirstURL)] = true
		case differInSecond:
			second[filepath.Clean(diff.SecondURL)] = true
		default:
			c.Fatalf("unexpected mirror difference: %v", diff.Diff)
		}
	}
	c.Assert(first, checkv1.HasLen, 4)
	c.Assert(second, checkv1.DeepEquals, map[string]bool{
		filepath.Clean(targetOnlyDir): true,
	})
}
