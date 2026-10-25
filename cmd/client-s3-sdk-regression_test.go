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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func newSDKRegressionClient(t *testing.T, server *httptest.Server) *S3Client {
	t.Helper()
	api, err := minio.New(server.Listener.Addr().String(), &minio.Options{
		Creds:     credentials.NewStaticV4("access", "secret", ""),
		Region:    "us-east-1",
		Transport: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &S3Client{api: api, targetURL: newClientURL(server.URL + "/bucket/object")}
}

// Exercise mcli's Put wrapper: a truncated source must never become a
// successfully completed object, and the outstanding upload must be aborted.
func TestS3PutStreamLength(t *testing.T) {
	const partSize = 5 << 20
	const size = 7 << 20
	for _, parallel := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			declared  int64
			available int
			wantErr   bool
		}{
			{"exact", size, size, false},
			{"short part", size, size - 1, true},
			{"short at part boundary", size, partSize, true},
			{"empty source", size, 0, true},
			{"unknown length", -1, size, false},
		} {
			t.Run("parallel="+strconv.FormatBool(parallel)+"/"+tc.name, func(t *testing.T) {
				var puts, shortParts, aborts, completes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					q := r.URL.Query()
					switch {
					case r.Method == http.MethodPost && q.Has("uploads"):
						_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>test</UploadId></InitiateMultipartUploadResult>`)
					case r.Method == http.MethodPut && q.Get("uploadId") == "test":
						puts.Add(1)
						part, _ := strconv.Atoi(q.Get("partNumber"))
						if tc.wantErr && part > tc.available/partSize {
							shortParts.Add(1)
						}
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("ETag", `"part-etag"`)
					case r.Method == http.MethodPost && q.Get("uploadId") == "test":
						completes.Add(1)
						_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
					case r.Method == http.MethodDelete && q.Get("uploadId") == "test":
						aborts.Add(1)
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				client := newSDKRegressionClient(t, server)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				// Hide ReadAt to model a streamed source rather than a local file.
				reader := struct{ io.Reader }{bytes.NewReader(make([]byte, tc.available))}
				written, err := client.Put(ctx, reader, tc.declared, nil, PutOptions{
					multipartSize: partSize, multipartThreads: 2, concurrentStream: parallel,
				})
				if tc.wantErr {
					if err == nil {
						t.Fatal("truncated input reported success")
					}
					if _, ok := err.ToGoError().(UnexpectedEOF); !ok {
						t.Errorf("error = %v, want mcli UnexpectedEOF", err)
					}
					if shortParts.Load() != 0 || completes.Load() != 0 || aborts.Load() != 1 {
						t.Errorf("short parts=%d, completes=%d, aborts=%d; want 0, 0, 1", shortParts.Load(), completes.Load(), aborts.Load())
					}
					return
				}
				if err != nil || written != int64(tc.available) {
					t.Fatalf("written=%d, error=%v; want %d bytes", written, err, tc.available)
				}
				if puts.Load() != 2 || completes.Load() != 1 || aborts.Load() != 0 {
					t.Errorf("puts=%d, completes=%d, aborts=%d; want 2, 1, 0", puts.Load(), completes.Load(), aborts.Load())
				}
			})
		}
	}
}

// Keys containing XML control characters use the SDK's single-delete fallback.
// mcli must receive exactly one failed result for each rejected object.
func TestS3RemoveReportsFailureOnce(t *testing.T) {
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/bucket/bad\x01key" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deletes.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`)
	}))
	defer server.Close()
	client := newSDKRegressionClient(t, server)
	contents := make(chan *ClientContent, 1)
	contents <- &ClientContent{URL: ClientURL{Path: "/bucket/bad\x01key"}}
	close(contents)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var results []RemoveResult
	for result := range client.Remove(ctx, false, false, false, false, contents) {
		results = append(results, result)
	}
	if len(results) != 1 || deletes.Load() != 1 {
		t.Fatalf("results=%d, DELETE requests=%d; want 1 each", len(results), deletes.Load())
	}
	if results[0].Err == nil || !strings.Contains(results[0].Err.ToGoError().Error(), "Access Denied") {
		t.Fatalf("result error = %v, want Access Denied", results[0].Err)
	}
}
