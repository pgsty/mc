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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type lsTestRow struct {
	Status         string
	Key            string
	Size           int64
	VersionID      string `json:"versionId"`
	VersionOrd     int    `json:"versionOrdinal"`
	IsDeleteMarker bool
	TotalObjects   int64
	TotalSize      int64
}

func lsTestRows(t *testing.T, endpoint string, args ...string) []lsTestRow {
	t.Helper()
	result := runChecksumVerifyCLI(t, endpoint, []string{"MC_REGION=us-east-1"}, append([]string{"--json", "ls"}, args...)...)
	if result.exitCode != 0 {
		t.Fatalf("ls %v exited %d: %s%s", args, result.exitCode, result.stdout, result.stderr)
	}
	return decodeLSRows(t, result.stdout)
}

func decodeLSRows(t *testing.T, output []byte) []lsTestRow {
	t.Helper()
	var rows []lsTestRow
	for line := range bytes.Lines(output) {
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
		lsTestRow{Status: "success", Key: "sub/large", Size: 8, VersionOrd: 1},
		lsTestRow{Status: "success", Key: "a", Size: 1, VersionOrd: 1},
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
		if r.URL.Query().Get("key-marker") == "b" {
			if strings.Trim(r.URL.Path, "/") == "partial" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>second page denied</Message></Error>`)
				return
			}
			_, _ = io.WriteString(w, `<ListVersionsResult>
<Version><Key>c</Key><VersionId>empty</VersionId><IsLatest>true</IsLatest><Size>0</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Version>
</ListVersionsResult>`)
			return
		}
		_, _ = io.WriteString(w, `<ListVersionsResult>
<IsTruncated>true</IsTruncated><NextKeyMarker>b</NextKeyMarker><NextVersionIdMarker>prior</NextVersionIdMarker>
<Version><Key>a</Key><VersionId>new</VersionId><IsLatest>true</IsLatest><Size>1</Size><LastModified>2026-11-02T00:00:00Z</LastModified></Version>
<Version><Key>a</Key><VersionId>old</VersionId><Size>4294967360</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Version>
<DeleteMarker><Key>b</Key><VersionId>deleted</VersionId><IsLatest>true</IsLatest><LastModified>2026-11-02T00:00:00Z</LastModified></DeleteMarker>
<Version><Key>b</Key><VersionId>prior</VersionId><Size>64</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Version>
</ListVersionsResult>`)
	}))
	defer server.Close()

	got := lsTestRows(t, server.URL, "--versions", "--sort=size", "b4/bucket/")
	want := []lsTestRow{
		{Status: "success", Key: "a", Size: 4294967360, VersionID: "old", VersionOrd: 1},
		{Status: "success", Key: "b", Size: 64, VersionID: "prior", VersionOrd: 1},
		{Status: "success", Key: "a", Size: 1, VersionID: "new", VersionOrd: 2},
		{Status: "success", Key: "b", VersionID: "deleted", VersionOrd: 2, IsDeleteMarker: true},
		{Status: "success", Key: "c", VersionID: "empty", VersionOrd: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted versions = %+v, want %+v", got, want)
	}

	args := []string{"ls", "--versions", "--sort=size", "--summarize", "b4/partial/"}
	env := []string{"MC_REGION=us-east-1"}
	result := runChecksumVerifyCLI(t, server.URL, env, append([]string{"--json"}, args...)...)
	if result.exitCode != 1 || !bytes.Contains(result.stdout, []byte("second page denied")) {
		t.Fatalf("partial JSON listing exited %d: %s%s", result.exitCode, result.stdout, result.stderr)
	}
	partial := append([]lsTestRow{{Status: "error"}}, want[:4]...)
	partial = append(partial, lsTestRow{TotalObjects: 4, TotalSize: 4294967360 + 64 + 1})
	if got := decodeLSRows(t, result.stdout); !slices.Equal(got, partial) {
		t.Fatalf("partial JSON rows = %+v, want %+v", got, partial)
	}

	result = runChecksumVerifyCLI(t, server.URL, env, args...)
	if result.exitCode != 1 || !bytes.Contains(result.stderr, []byte("second page denied")) {
		t.Fatalf("partial text listing exited %d: %s%s", result.exitCode, result.stdout, result.stderr)
	}
	lines := strings.FieldsFunc(string(result.stdout), func(r rune) bool { return r == '\n' || r == '\r' })
	suffixes := []string{" old v1 PUT a", " prior v1 PUT b", " new v2 PUT a", " deleted v2 DEL b", "Total Size: 4.0 GiB", "Total Objects: 4"}
	if len(lines) != len(suffixes) {
		t.Fatalf("partial text listing has %d lines: %s", len(lines), result.stdout)
	}
	for i, suffix := range suffixes {
		if !strings.HasSuffix(lines[i], suffix) {
			t.Errorf("line %q should end with %q", lines[i], suffix)
		}
	}
}

func TestLSInvalidSort(t *testing.T) {
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, "--json", "ls", "--sort=bogus", "b4/bucket/")
	if result.exitCode == 0 || !bytes.Contains(result.stdout, []byte("Use --sort=size")) {
		t.Fatalf("ls --sort=bogus exited %d: %s%s", result.exitCode, result.stdout, result.stderr)
	}
}

func TestLSStreamsUnlessSorted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		stream bool
		keys   []string
	}{
		{"ls", []string{"ls", "-r"}, true, []string{"a", "b", "c", "d"}},
		{"tree", []string{"tree"}, true, []string{"a", "b", "c", "d"}},
		{"size", []string{"ls", "-r", "--sort=size"}, false, []string{"d", "b", "c", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstRow := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead { // tree checks the bucket before listing it.
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				if r.URL.Query().Get("continuation-token") == "" {
					_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken>
<Contents><Key>a</Key><Size>1</Size></Contents><Contents><Key>b</Key><Size>4</Size></Contents></ListBucketResult>`)
					return
				}
				if tc.stream {
					select {
					case <-firstRow:
					case <-time.After(10 * time.Second):
						t.Error("no object row reached stdout while page two was withheld")
					}
				}
				_, _ = io.WriteString(w, `<ListBucketResult><Contents><Key>c</Key><Size>4</Size></Contents>
<Contents><Key>d</Key><Size>8</Size></Contents></ListBucketResult>`)
			}))
			defer server.Close()

			args := append([]string{"--json"}, tc.args...)
			command := checksumVerifyCLICommand(t, server.URL, []string{"MC_REGION=us-east-1"}, append(args, "b4/bucket/")...)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			var keys []string
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				var row lsTestRow
				if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
					t.Errorf("invalid row %q: %v", scanner.Text(), err)
					continue
				}
				if len(keys) == 0 {
					close(firstRow)
				}
				keys = append(keys, row.Key)
			}
			if err := scanner.Err(); err != nil {
				t.Error(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("listing failed: %v: %s", err, &stderr)
			}
			if !slices.Equal(keys, tc.keys) {
				t.Fatalf("listed %v, want %v", keys, tc.keys)
			}
		})
	}
}
