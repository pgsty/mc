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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A zero-length upload carries no trailer, so the pinned SDK's trailer gate
// (contentLength > 0) drops an explicitly requested checksum unless mc supplies
// it as a plain header instead. TestEmptyPipeRetainsExplicitChecksum guards
// (*S3Client).Put's size==0 header fallback for the `pipe` path with CRC32C
// and SHA256.
func TestEmptyPipeRetainsExplicitChecksum(t *testing.T) {
	for _, algorithm := range []struct{ name, header, value string }{
		{"CRC32C", "X-Amz-Checksum-Crc32c", "AAAAAA=="},
		{"SHA256", "X-Amz-Checksum-Sha256", "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="},
	} {
		t.Run(algorithm.name, func(t *testing.T) {
			var mu sync.Mutex
			var checksum string
			var puts int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Query().Has("location") {
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
					return
				}
				if r.Method != http.MethodPut || r.URL.Path != "/archive/empty" || r.URL.RawQuery != "" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				mu.Lock()
				puts++
				checksum = r.Header.Get(algorithm.header)
				if checksum == "" {
					checksum = r.Trailer.Get(algorithm.header)
				}
				mu.Unlock()
				w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
			}))
			defer server.Close()
			result := runChecksumVerifyCLI(t, server.URL, nil, "pipe", "--checksum", algorithm.name, "b4/archive/empty")
			if result.exitCode != 0 {
				t.Fatalf("pipe exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			mu.Lock()
			defer mu.Unlock()
			if puts != 1 || checksum != algorithm.value {
				t.Fatalf("empty pipe requested %s: PUTs=%d, checksum=%q; want one regular PUT with %q", algorithm.name, puts, checksum, algorithm.value)
			}
		})
	}

	// Same size==0 header fallback, exercised through `cp` of an empty local file
	// instead of piped stdin, since (*S3Client).Put is the shared choke point for
	// cp, put, mirror and mv. Unlike pipe, cp probes the destination first (bucket
	// object-lock config, a HEAD on the target key, and a prefix listing to rule
	// out a directory target), so the mock answers those "not found" before the
	// single expected PUT.
	t.Run("CRC32C-cp", func(t *testing.T) {
		const header = "X-Amz-Checksum-Crc32c"
		const value = "AAAAAA=="
		var mu sync.Mutex
		var checksum string
		var puts int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case r.Method == http.MethodGet && q.Has("location"):
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
				return
			case r.Method == http.MethodGet && q.Has("object-lock"):
				w.WriteHeader(http.StatusNotFound)
				return
			case r.Method == http.MethodHead && r.URL.Path == "/archive/empty":
				w.WriteHeader(http.StatusNotFound)
				return
			case r.Method == http.MethodGet && q.Get("list-type") == "2":
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
					`<Name>archive</Name><Prefix>`+q.Get("prefix")+`</Prefix><MaxKeys>1</MaxKeys><IsTruncated>false</IsTruncated></ListBucketResult>`)
				return
			case r.Method == http.MethodPut && r.URL.Path == "/archive/empty" && r.URL.RawQuery == "":
				_, _ = io.Copy(io.Discard, r.Body)
				mu.Lock()
				puts++
				checksum = r.Header.Get(header)
				if checksum == "" {
					checksum = r.Trailer.Get(header)
				}
				mu.Unlock()
				w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		emptyFile := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		result := runChecksumVerifyCLI(t, server.URL, nil, "cp", "--checksum", "CRC32C", emptyFile, "b4/archive/empty")
		if result.exitCode != 0 {
			t.Fatalf("cp exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		mu.Lock()
		defer mu.Unlock()
		if puts != 1 || checksum != value {
			t.Fatalf("empty cp requested CRC32C: PUTs=%d, checksum=%q; want one regular PUT with %q", puts, checksum, value)
		}
	})
}
