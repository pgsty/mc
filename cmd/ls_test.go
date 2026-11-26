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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/pgsty/silo-pkg/v3/console"
)

type lsTestRow struct {
	Key          string
	Size         int64
	VersionID    string `json:"versionId"`
	VersionOrd   int    `json:"versionOrdinal"`
	TotalObjects int64
	TotalSize    int64
}

func lsTestRows(t *testing.T, endpoint string, args ...string) []lsTestRow {
	t.Helper()
	result := runChecksumVerifyCLI(t, endpoint, []string{"MC_REGION=us-east-1"}, append([]string{"--json", "ls"}, args...)...)
	if result.exitCode != 0 {
		t.Fatalf("ls %v exited %d: %s%s", args, result.exitCode, result.stdout, result.stderr)
	}
	var rows []lsTestRow
	for line := range bytes.Lines(result.stdout) {
		var row lsTestRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("invalid ls row %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestLSSortBySize(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	var total int64
	for i := range 32 {
		size := i % 3
		total += int64(size)
		if err := os.WriteFile(filepath.Join(first, fmt.Sprintf("key-%02d", i)), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(second, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a": 1, "sub/large": 8} {
		if err := os.WriteFile(filepath.Join(second, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const endpoint = "http://localhost"
	plain := lsTestRows(t, endpoint, "--recursive", first)
	if len(plain) != 32 || !slices.Equal(plain, lsTestRows(t, endpoint, "--recursive", "--sort=", first)) {
		t.Fatal("ordinary and empty-sort listings should contain the same 32 entries in the same order")
	}
	var want []lsTestRow
	for size := int64(2); size >= 0; size-- {
		for _, row := range plain {
			if row.Size == size {
				want = append(want, row)
			}
		}
	}
	want = append(want,
		lsTestRow{TotalObjects: 32, TotalSize: total},
		lsTestRow{Key: "sub/large", Size: 8, VersionOrd: 1},
		lsTestRow{Key: "a", Size: 1, VersionOrd: 1},
		lsTestRow{TotalObjects: 2, TotalSize: 9},
	)
	got := lsTestRows(t, endpoint, "--recursive", "--sort=size", "--summarize", first, second)
	if !slices.Equal(got, want) {
		t.Fatalf("sorted listing:\n%+v\nwant:\n%+v", got, want)
	}
}

func TestLSSortBySizeVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !r.URL.Query().Has("versions") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<ListVersionsResult>
<Version><Key>a</Key><VersionId>new</VersionId><IsLatest>true</IsLatest><Size>1</Size><LastModified>2026-11-02T00:00:00Z</LastModified></Version>
<Version><Key>a</Key><VersionId>old</VersionId><Size>4294967360</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Version>
<Version><Key>b</Key><VersionId>only</VersionId><IsLatest>true</IsLatest><Size>64</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Version>
</ListVersionsResult>`)
	}))
	defer server.Close()

	got := lsTestRows(t, server.URL, "--versions", "--sort=size", "b4/bucket/")
	want := []lsTestRow{
		{Key: "a", Size: 4294967360, VersionID: "old", VersionOrd: 1},
		{Key: "b", Size: 64, VersionID: "only", VersionOrd: 1},
		{Key: "a", Size: 1, VersionID: "new", VersionOrd: 2},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted versions = %+v, want %+v", got, want)
	}
}

func TestLSInvalidSort(t *testing.T) {
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, "--json", "ls", "--sort=bogus", "b4/bucket/")
	if result.exitCode == 0 || !bytes.Contains(result.stdout, []byte("Use --sort=size")) {
		t.Fatalf("ls --sort=bogus exited %d: %s%s", result.exitCode, result.stdout, result.stderr)
	}
}

type lsStreamTestClient struct {
	Client
	exhausted atomic.Bool
}

func (*lsStreamTestClient) GetURL() ClientURL { return *newClientURL("http://localhost/bucket/") }

func (c *lsStreamTestClient) List(context.Context, ListOptions) <-chan *ClientContent {
	rows := make(chan *ClientContent)
	go func() {
		// The third send cannot complete until doList has processed the second
		// key and had a chance to print the first one.
		for _, key := range []string{"a", "b", "c"} {
			rows <- &ClientContent{URL: *newClientURL("http://localhost/bucket/" + key)}
		}
		c.exhausted.Store(true)
		close(rows)
	}()
	return rows
}

func TestLSStreamsUnlessSorted(t *testing.T) {
	original := console.Println
	t.Cleanup(func() { console.Println = original })
	for _, sortBySize := range []bool{false, true} {
		client := &lsStreamTestClient{}
		var beforeEnd bool
		var count int
		console.Println = func(...any) {
			count++
			if !client.exhausted.Load() {
				beforeEnd = true
			}
		}
		if err := doList(t.Context(), client, doListOptions{sortBySize: sortBySize}); err != nil {
			t.Fatal(err)
		}
		wantBeforeEnd := !sortBySize
		if count != 3 || beforeEnd != wantBeforeEnd {
			t.Fatalf("sortBySize=%t: printed %d rows, output before end=%t", sortBySize, count, beforeEnd)
		}
	}
}
