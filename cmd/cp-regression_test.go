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
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// This fixture keeps different source bytes at identical keys on two endpoints.
// Checking copied bytes and surviving sources detects both wrong reads and
// wrong delete routing, even when every HTTP request succeeds.
func TestCopyMoveMultipleAliases(t *testing.T) {
	for _, command := range []string{"cp", "mv"} {
		t.Run(command, func(t *testing.T) {
			var mu sync.Mutex
			objects := []map[string]string{
				{"/source/first": "A-first", "/source/second": "A-decoy"},
				{"/source/second": "B-correct"},
			}
			newServer := func(index int) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					w.Header().Set("Content-Type", "application/xml")
					w.Header().Set("ETag", `"etag"`)
					w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
					switch {
					case r.URL.Query().Has("location"):
						_, _ = io.WriteString(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
					case r.URL.Query().Has("object-lock"):
						_, _ = io.WriteString(w, `<ObjectLockConfiguration/>`)
					case r.Method == http.MethodGet && r.URL.Query().Has("list-type"):
						_, _ = fmt.Fprint(w, `<ListBucketResult><Name>source</Name><IsTruncated>false</IsTruncated>`)
						prefix := strings.TrimSuffix(r.URL.Path, "/") + "/" + r.URL.Query().Get("prefix")
						for key, value := range objects[index] {
							if strings.HasPrefix(key, prefix) {
								_, _ = fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"etag"</ETag><LastModified>2026-01-01T00:00:00Z</LastModified></Contents>`, strings.TrimPrefix(key, strings.TrimSuffix(r.URL.Path, "/")+"/"), len(value))
							}
						}
						_, _ = fmt.Fprint(w, `</ListBucketResult>`)
					case r.Method == http.MethodHead:
						if value, ok := objects[index][r.URL.Path]; ok {
							w.Header().Set("Content-Length", fmt.Sprint(len(value)))
						} else if strings.Count(r.URL.Path, "/") > 1 {
							w.Header().Set("X-Minio-Error-Code", "NoSuchKey")
							w.WriteHeader(http.StatusNotFound)
						}
					case r.Method == http.MethodGet:
						value, ok := objects[index][r.URL.Path]
						if !ok {
							http.NotFound(w, r)
							return
						}
						w.Header().Set("Content-Length", fmt.Sprint(len(value)))
						_, _ = io.WriteString(w, value)
					case r.Method == http.MethodPut:
						if source := r.Header.Get("X-Amz-Copy-Source"); source != "" {
							source, _ = url.PathUnescape(source)
							objects[index][r.URL.Path] = objects[index]["/"+strings.TrimPrefix(source, "/")]
							_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"etag"</ETag><LastModified>2026-01-01T00:00:00Z</LastModified></CopyObjectResult>`)
						} else {
							var body io.Reader = r.Body
							if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
								body = httputil.NewChunkedReader(body)
							}
							data, err := io.ReadAll(body)
							if err != nil {
								t.Error(err)
							}
							objects[index][r.URL.Path] = string(data)
						}
					case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
						var deletion struct {
							Objects []struct{ Key string } `xml:"Object"`
						}
						if err := xml.NewDecoder(r.Body).Decode(&deletion); err != nil {
							t.Error(err)
						}
						_, _ = io.WriteString(w, `<DeleteResult>`)
						for _, object := range deletion.Objects {
							delete(objects[index], strings.TrimSuffix(r.URL.Path, "/")+"/"+object.Key)
							_, _ = fmt.Fprintf(w, `<Deleted><Key>%s</Key></Deleted>`, object.Key)
						}
						_, _ = io.WriteString(w, `</DeleteResult>`)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
			}
			first, second := newServer(0), newServer(1)
			defer first.Close()
			defer second.Close()
			env := []string{"MC_REGION=us-east-1", "MC_HOST_peer=http://access2:secret2@" + strings.TrimPrefix(second.URL, "http://")}
			result := runChecksumVerifyCLI(t, first.URL, env, "--json", command, "b4/source/first", "peer/source/second", "b4/target/")
			if result.exitCode != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			mu.Lock()
			defer mu.Unlock()
			if objects[0]["/target/first"] != "A-first" || objects[0]["/target/second"] != "B-correct" {
				t.Fatalf("wrong copied content: %+v; stdout=%s stderr=%s", objects, result.stdout, result.stderr)
			}
			_, firstExists := objects[0]["/source/first"]
			_, secondExists := objects[1]["/source/second"]
			if firstExists != (command == "cp") || secondExists != (command == "cp") || objects[0]["/source/second"] != "A-decoy" {
				t.Fatalf("wrong sources removed: %+v", objects)
			}
		})
	}
}
