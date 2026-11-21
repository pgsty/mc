// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
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
	"testing"
)

// TestLSSortBySize verifies that listContents with sortBy set to "size"
// returns the listed objects in ascending size order.
func TestLSSortBySize(t *testing.T) {
	dir := t.TempDir()

	// Create files of distinct sizes, intentionally not in size order.
	files := []struct {
		name string
		size int
	}{
		{"a", 10},   // small
		{"b", 4096}, // large
		{"c", 1024}, // medium
		{"d", 1},    // tiny
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), bytes.Repeat([]byte("x"), f.size), 0o600); err != nil {
			t.Fatalf("unable to create test file %s: %v", f.name, err)
		}
	}

	// A trailing separator is required for the fs client to list inside the directory.
	clnt, err := fsNew(dir + string(filepath.Separator))
	if err != nil {
		t.Fatalf("unable to create fs client: %v", err)
	}

	contents, totalObjects, totalSize, listErr := listContents(context.Background(), clnt, doListOptions{sortBy: "size"})
	if listErr != nil {
		t.Fatalf("listContents failed: %v", listErr)
	}

	var keys []string
	for _, content := range contents {
		keys = append(keys, content.Key)
	}
	// Sorted listing is in ascending size order: d(1B), a(10B), c(1KiB), b(4KiB).
	expectedKeys := []string{"d", "a", "c", "b"}
	if !slices.Equal(keys, expectedKeys) {
		t.Errorf("expected keys %q in ascending size order, got %q", expectedKeys, keys)
	}

	var expectedTotalSize int64
	for _, f := range files {
		expectedTotalSize += int64(f.size)
	}
	if totalObjects != int64(len(files)) {
		t.Errorf("expected %d total objects, got %d", len(files), totalObjects)
	}
	if totalSize != expectedTotalSize {
		t.Errorf("expected total size %d, got %d", expectedTotalSize, totalSize)
	}
}
