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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"github.com/minio/minio-go/v7/pkg/notification"
)

func TestMirrorDeleteChecksCurrentSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		code        string
		marker      bool
		cancel      bool
		clientError bool
		dryRun      bool
		wantDeleted bool
		wantError   bool
	}{
		{name: "current version exists", status: http.StatusOK},
		{name: "object deleted", status: http.StatusNotFound, code: "NoSuchKey", wantDeleted: true},
		{name: "current delete marker", status: http.StatusNotFound, code: "NoSuchKey", marker: true, wantDeleted: true},
		{name: "access denied", status: http.StatusForbidden, code: "AccessDenied", wantError: true},
		{name: "bucket missing", status: http.StatusNotFound, code: "NoSuchBucket", wantError: true},
		{name: "server failure", status: http.StatusInternalServerError, code: "InternalError", wantError: true},
		{name: "canceled", cancel: true, wantError: true},
		{name: "client creation failed", clientError: true, wantError: true},
		{name: "dry run", status: http.StatusNotFound, code: "NoSuchKey", dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var heads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.URL.Path != "/bucket/object" {
					t.Errorf("unexpected source request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				heads.Add(1)
				w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
				w.Header().Set("X-Minio-Error-Code", tc.code)
				if tc.marker {
					w.Header().Set("X-Amz-Delete-Marker", "true")
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			t.Setenv("MC_HOST_mirrortest", "http://access:secret@"+strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("MC_REGION", "us-east-1")
			if tc.clientError {
				oldNew := S3New
				S3New = func(*Config) (Client, *probe.Error) {
					return nil, probe.NewError(errors.New("cannot create source client"))
				}
				defer func() { S3New = oldNew }()
			}
			target := t.TempDir()
			file := filepath.Join(target, "object")
			if err := os.WriteFile(file, []byte("current version"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			event := EventInfo{
				Type: notification.ObjectRemovedDelete, Path: server.URL + "/bucket/object",
			}
			result := runMirrorDeleteEvent(ctx, t, "mirrortest/bucket", target, event, mirrorOptions{isRemove: true, isFake: tc.dryRun})
			if tc.status == http.StatusOK {
				// Redelivery of an old-version deletion must also preserve the target.
				result = runMirrorDeleteEvent(ctx, t, "mirrortest/bucket", target, event, mirrorOptions{isRemove: true})
			}
			if (result.Error != nil) != tc.wantError {
				t.Errorf("error = %v, want error %v", result.Error, tc.wantError)
			}
			if result.SourceContent != nil {
				t.Fatal("delete result changed into a source-copy result")
			}
			data, err := os.ReadFile(file)
			if tc.wantDeleted {
				if !os.IsNotExist(err) {
					t.Fatalf("target still present: data=%q err=%v", data, err)
				}
			} else if err != nil || string(data) != "current version" {
				t.Fatalf("target was changed: data=%q err=%v", data, err)
			}
			if !tc.cancel && !tc.clientError && !tc.dryRun && heads.Load() == 0 {
				t.Error("source was never checked")
			}
		})
	}
}

func runMirrorDeleteEvent(ctx context.Context, t *testing.T, source, target string, event EventInfo, opts mirrorOptions) URLs {
	t.Helper()
	oldLoad := loadMcConfig
	loadMcConfig = func() (*configV10, *probe.Error) { return &configV10{}, nil }
	defer func() { loadMcConfig = oldLoad }()
	results := make(chan URLs, 1)
	parallel := newParallelManager(results, 1)
	defer parallel.stopAndWait()
	status := NewQuietStatus(parallel)
	defer status.(*QuietStatus).Stat()
	job := &mirrorJob{sourceURL: source, targetURL: target, opts: opts, parallel: parallel, status: status}
	job.watchMirrorEvents(ctx, []EventInfo{event})
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("delete task did not finish")
		return URLs{}
	}
}

func TestMirrorDeletePreservesExactKeyAndSSE(t *testing.T) {
	for _, key := range []string{"prefix/", "object", "dir/space + 中文"} {
		for _, eventType := range []notification.EventType{notification.ObjectRemovedDelete, notification.ObjectRemovedDeleteMarkerCreated, notification.ILMDelMarkerExpirationDelete} {
			t.Run(key+"/"+string(eventType), func(t *testing.T) {
				var heads atomic.Int32
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodHead || r.URL.Path != "/bucket/"+key {
						t.Errorf("unexpected source request: %s %s", r.Method, r.URL)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if r.URL.Query().Has("versionId") {
						t.Error("checked a historical version")
					}
					if r.Header.Get("X-Amz-Server-Side-Encryption-Customer-Key") == "" {
						t.Error("source HEAD omitted SSE-C")
					}
					heads.Add(1)
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				t.Setenv("MC_HOST_mirrortest", "https://access:secret@"+strings.TrimPrefix(server.URL, "https://"))
				t.Setenv("MC_REGION", "us-east-1")
				oldInsecure := globalInsecure
				globalInsecure = true
				defer func() { globalInsecure = oldInsecure }()
				sse, err := encrypt.NewSSEC([]byte(strings.Repeat("k", 32)))
				if err != nil {
					t.Fatal(err)
				}
				// The S3 listen API supplies raw keys. Use the real notification conversion.
				var info notification.Info
				raw, err := json.Marshal(map[string]any{"Records": []any{map[string]any{
					"eventName": eventType, "s3": map[string]any{
						"bucket": map[string]string{"name": "bucket"}, "object": map[string]string{"key": key},
					},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &info); err != nil {
					t.Fatal(err)
				}
				client := &S3Client{targetURL: newClientURL(server.URL)}
				events := client.notificationToEventsInfo(info)
				if events[0].Path != server.URL+"/bucket/"+key {
					t.Fatalf("notification changed key: %q", events[0].Path)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				result := runMirrorDeleteEvent(ctx, t, "mirrortest/bucket", t.TempDir(), events[0], mirrorOptions{
					isRemove: true, encKeyDB: map[string][]prefixSSEPair{"mirrortest": {{Prefix: "mirrortest/bucket/", SSE: sse}}},
				})
				if result.Error != nil || heads.Load() != 1 {
					t.Fatalf("HEAD count=%d error=%v", heads.Load(), result.Error)
				}
			})
		}
	}
}

func TestMirrorDeleteFilesystemSource(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	file := filepath.Join(target, "object")
	if err := os.WriteFile(file, []byte("deleted source"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runMirrorDeleteEvent(context.Background(), t, source, target, EventInfo{
		Type: notification.ObjectRemovedDelete, Path: filepath.Join(source, "object"),
	}, mirrorOptions{isRemove: true})
	if _, err := os.Stat(file); !os.IsNotExist(err) || result.Error != nil {
		t.Fatalf("filesystem delete failed: stat=%v task=%v", err, result.Error)
	}
}

func TestNotificationBucketPathUnchanged(t *testing.T) {
	var info notification.Info
	if err := json.NewDecoder(strings.NewReader(`{"Records":[{"eventName":"s3:BucketCreated:*","s3":{"bucket":{"name":"bucket"},"object":{"key":""}}}]}`)).Decode(&info); err != nil {
		t.Fatal(err)
	}
	client := &S3Client{targetURL: newClientURL("http://localhost")}
	if got := client.notificationToEventsInfo(info)[0].Path; got != "http://localhost/bucket" {
		t.Fatalf("bucket event path = %q", got)
	}
}
