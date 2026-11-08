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
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/cli"
	"github.com/minio/mc/pkg/probe"
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

func TestCopyMovePreparationFailures(t *testing.T) {
	for _, command := range []string{"cp", "mv"} {
		for _, missingFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing-first=%v", command, missingFirst), func(t *testing.T) {
				dir := t.TempDir()
				good, missing, target := filepath.Join(dir, "good"), filepath.Join(dir, "missing"), filepath.Join(dir, "target")
				if err := os.WriteFile(good, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				sources := []string{good, missing}
				if missingFirst {
					sources[0], sources[1] = sources[1], sources[0]
				}
				args := append([]string{"--json", command}, sources...)
				args = append(args, target)
				result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, args...)
				if result.exitCode != 1 || !strings.Contains(string(result.stdout), "Unable to prepare") {
					t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
				}
				if data, err := os.ReadFile(filepath.Join(target, "good")); err != nil || string(data) != "keep" {
					t.Fatalf("valid source not copied: %q %v", data, err)
				}
				_, err := os.Stat(good)
				if (err == nil) != (command == "cp") {
					t.Fatalf("unexpected source state after %s: %v", command, err)
				}
			})
		}
	}
}

func TestCopyStreamingTotals(t *testing.T) {
	const count, size = 200, 13
	source, target := t.TempDir(), t.TempDir()
	for i := range count {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("%03d", i)), bytes.Repeat([]byte("a"), size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, "--json", "cp", "--recursive", source+string(filepath.Separator), target)
	if result.exitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	seen := make(map[int64]bool)
	var total, transferred int64
	for {
		var record struct {
			Source      string
			TotalCount  int64
			TotalSize   int64
			Total       int64
			Transferred int64
		}
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if record.Source != "" {
			if record.TotalCount < 1 || record.TotalCount > count || record.TotalSize != record.TotalCount*size || seen[record.TotalCount] {
				t.Fatalf("bad cumulative totals: %+v", record)
			}
			seen[record.TotalCount] = true
		} else {
			total, transferred = record.Total, record.Transferred
		}
	}
	if len(seen) != count || total != count*size || transferred != count*size {
		t.Fatalf("objects=%d total=%d transferred=%d", len(seen), total, transferred)
	}
}

func TestCopyCancellationDrainsWorkers(t *testing.T) {
	oldLoad := loadMcConfig
	defer func() { loadMcConfig = oldLoad }()
	loadMcConfig = func() (*configV10, *probe.Error) { return &configV10{}, nil }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, released := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Has("location"):
			_, _ = io.WriteString(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "7")
			w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
			w.Header().Set("ETag", `"etag"`)
		case r.Method == http.MethodGet:
			once.Do(func() { close(started) })
			<-r.Context().Done()
			close(released)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	t.Setenv("MC_HOST_cancelcopy", "http://access:secret@"+strings.TrimPrefix(server.URL, "http://"))
	target := filepath.Join(t.TempDir(), "target")
	flags := flag.NewFlagSet("cp", flag.ContinueOnError)
	for _, f := range cpFlags {
		f.Apply(flags)
	}
	if err := flags.Parse([]string{"cancelcopy/bucket/source", target}); err != nil {
		t.Fatal(err)
	}
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	captureCommandJSON(t, func() {
		err := doCopySession(ctx, cancel, cli.NewContext(cli.NewApp(), flags, nil), nil, false)
		var exit cli.ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != globalCancelExitStatus {
			t.Errorf("canceled copy returned %v", err)
		}
	})
	if ctx.Err() != context.Canceled {
		t.Fatal("copy did not cancel during the transfer")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("request was not released")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("canceled copy left destination: %v", err)
	}
}
