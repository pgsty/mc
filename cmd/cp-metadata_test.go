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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Exercise the CLI through signed HTTP requests: server-side copy must use
// tagging replacement, and stream copy must give explicit tags precedence
// over preserved source tags. An explicit empty value clears tags.
func TestCopyMoveTags(t *testing.T) {
	for _, command := range []string{"cp", "mv"} {
		for _, stream := range []bool{false, true} {
			for _, preserve := range []bool{false, true} {
				for _, tagCase := range []string{"unset", "replace", "clear", "invalid"} {
					t.Run(fmt.Sprintf("%s/stream=%v/preserve=%v/%s", command, stream, preserve, tagCase), func(t *testing.T) {
						if preserve && runtime.GOOS == "windows" {
							t.Skip("--preserve is not supported on Windows")
						}
						const payload = "source bytes"
						var mu sync.Mutex
						var headers http.Header
						var copied string
						var deleted bool
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
								_, _ = io.WriteString(w, `<ListBucketResult><Name>bucket</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
							case r.Method == http.MethodHead || r.Method == http.MethodGet:
								switch r.URL.Path {
								case "/bucket/source":
									w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
									if r.Header.Get("X-Amz-Tagging-Directive") == "ACCESS" {
										w.Header().Set("X-Amz-Tagging", "owner=source")
									}
									if r.Method == http.MethodGet {
										_, _ = io.WriteString(w, payload)
									}
								case "/bucket/target":
									w.Header().Set("X-Minio-Error-Code", "NoSuchKey")
									w.WriteHeader(http.StatusNotFound)
								}
							case r.Method == http.MethodPut && r.URL.Path == "/bucket/target":
								headers = r.Header.Clone()
								if r.Header.Get("X-Amz-Copy-Source") != "" {
									copied = payload
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
									copied = string(data)
								}
							case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
								deleted = true
								_, _ = io.Copy(io.Discard, r.Body)
								_, _ = io.WriteString(w, `<DeleteResult><Deleted><Key>source</Key></Deleted></DeleteResult>`)
							default:
								t.Errorf("unexpected request: %s %s", r.Method, r.URL)
								w.WriteHeader(http.StatusBadRequest)
							}
						}))
						defer server.Close()
						args := []string{"--json", command}
						if preserve {
							args = append(args, "--preserve")
						}
						switch tagCase {
						case "replace":
							args = append(args, "--tags", "owner=target&note=hello+world")
						case "clear":
							args = append(args, "--tags=")
						case "invalid":
							args = append(args, "--tags", "=invalid")
						}
						target := "b4/bucket/target"
						if stream {
							target = "peer/bucket/target"
						}
						args = append(args, "b4/bucket/source", target)
						env := []string{"MC_REGION=us-east-1", "MC_HOST_peer=http://access:secret@" + strings.TrimPrefix(server.URL, "http://")}
						result := runChecksumVerifyCLI(t, server.URL, env, args...)
						mu.Lock()
						defer mu.Unlock()
						if tagCase == "invalid" {
							if result.exitCode != 1 || headers != nil || deleted {
								t.Fatalf("invalid tags: exit=%d headers=%v deleted=%v output=%s %s", result.exitCode, headers, deleted, result.stdout, result.stderr)
							}
							return
						}
						if result.exitCode != 0 || copied != payload || deleted != (command == "mv") {
							t.Fatalf("exit=%d copied=%q deleted=%v output=%s %s", result.exitCode, copied, deleted, result.stdout, result.stderr)
						}
						wantTags, wantDirective := "", ""
						if tagCase == "replace" {
							wantTags = "note=hello+world&owner=target"
						} else if tagCase == "unset" && stream && preserve {
							wantTags = "owner=source"
						}
						if !stream && tagCase != "unset" {
							wantDirective = "REPLACE"
						}
						gotTags, err := url.ParseQuery(headers.Get("X-Amz-Tagging"))
						if err != nil || gotTags.Encode() != wantTags || headers.Get("X-Amz-Tagging-Directive") != wantDirective {
							t.Errorf("tagging headers=%v, want tags=%q directive=%q (parse error=%v)", headers, wantTags, wantDirective, err)
						}
						if headers.Get("X-Amz-Meta-X-Amz-Tagging") != "" {
							t.Error("tags leaked into user metadata")
						}
					})
				}
			}
		}
	}
}

func TestCopyMoveRejectInvalidAttributesBeforeTransfer(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, command := range []string{"cp", "mv"} {
		for _, jsonOutput := range []bool{false, true} {
			for _, attr := range []string{"owner=alice;malformed", "owner=alice;malformed;", "owner='unterminated", ";"} {
				t.Run(fmt.Sprintf("%s/json=%v/%s", command, jsonOutput, attr), func(t *testing.T) {
					source := filepath.Join(t.TempDir(), "source")
					if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
					args := []string{command, "--attr", attr, source, "b4/bucket/target"}
					if jsonOutput {
						args = append([]string{"--json"}, args...)
					}
					result := runChecksumVerifyCLI(t, server.URL, nil, args...)
					if result.exitCode != 1 || !strings.Contains(string(result.stdout)+string(result.stderr), "Unable to parse --attr") {
						t.Fatalf("exit=%d output=%s %s", result.exitCode, result.stdout, result.stderr)
					}
					if data, err := os.ReadFile(source); err != nil || string(data) != "keep" {
						t.Fatalf("invalid attributes changed source: %q %v", data, err)
					}
					if requests.Load() != 0 {
						t.Fatalf("invalid attributes reached the server: %d requests", requests.Load())
					}
				})
			}
		}
	}
}

func TestCopyMoveValidAttributes(t *testing.T) {
	for _, test := range []struct{ command, attr string }{
		{"cp", "owner=alice;note='a;b=c';Content-Type=text/plain"},
		{"mv", "owner=alice;note='a;b=c';Content-Type=text/plain"},
		// Empty entries, such as a trailing separator, are ignored.
		{"cp", "owner=alice;;note='a;b=c';Content-Type=text/plain;"},
		{"mv", "owner=alice;;note='a;b=c';Content-Type=text/plain;"},
	} {
		command := test.command
		t.Run(command+"/"+test.attr, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			var uploads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.URL.Query().Has("object-lock"):
					_, _ = io.WriteString(w, `<ObjectLockConfiguration/>`)
				case r.Method == http.MethodHead:
					w.Header().Set("Content-Length", "0")
					w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
				case r.Method == http.MethodPut:
					_, _ = io.Copy(io.Discard, r.Body)
					uploads.Add(1)
					for key, want := range map[string]string{"X-Amz-Meta-Owner": "alice", "X-Amz-Meta-Note": "a;b=c", "Content-Type": "text/plain"} {
						if got := r.Header.Get(key); got != want {
							t.Errorf("%s=%q, want %q", key, got, want)
						}
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			result := runChecksumVerifyCLI(t, server.URL, []string{"MC_REGION=us-east-1"}, "--json", command,
				"--attr", test.attr, source, "b4/bucket/target")
			if result.exitCode != 0 || uploads.Load() != 1 {
				t.Fatalf("exit=%d uploads=%d output=%s %s", result.exitCode, uploads.Load(), result.stdout, result.stderr)
			}
			_, err := os.Stat(source)
			if (err == nil) != (command == "cp") {
				t.Fatalf("unexpected source state after %s: %v", command, err)
			}
		})
	}
}
