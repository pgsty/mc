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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/minio/madmin-go/v3"
)

func newResyncOperationTestServer(t *testing.T, op string, response []byte, requestError bool) *httptest.Server {
	t.Helper()
	var siteRequests, peerRequests, mutationRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /minio/admin/v3/site-replication/info":
			siteRequests.Add(1)
			_, _ = io.WriteString(w, `{"enabled":true,"sites":[{"deploymentID":"peer-site"}]}`)
		case "GET /minio/admin/v3/info":
			peerRequests.Add(1)
			_, _ = io.WriteString(w, `{"deploymentID":"peer-site","servers":[]}`)
		case "PUT /minio/admin/v3/site-replication/resync/op":
			mutationRequests.Add(1)
			if q := r.URL.Query(); q.Get("operation") != op || q.Get("api-version") != madmin.SiteReplAPIVersion {
				t.Errorf("unexpected resync options: %s", r.URL)
			}
			var peer madmin.PeerInfo
			if err := json.NewDecoder(r.Body).Decode(&peer); err != nil || peer.DeploymentID != "peer-site" {
				t.Errorf("unexpected resync peer: %+v, err=%v", peer, err)
			}
			if requestError {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"Code":"AccessDenied","Message":"resync mutation denied"}`)
				return
			}
			_, _ = w.Write(response)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if siteRequests.Load() != 1 || peerRequests.Load() != 1 || mutationRequests.Load() != 1 {
			t.Errorf("requests: site=%d peer=%d mutation=%d, want one each without retry",
				siteRequests.Load(), peerRequests.Load(), mutationRequests.Load())
		}
	})
	return server
}

func TestAdminReplicateResyncOperationsCLI(t *testing.T) {
	for _, op := range []string{"start", "cancel"} {
		for _, mode := range []string{"text", "json", "json_leaf_flag"} {
			for _, tc := range []struct {
				name         string
				status       string
				topError     string
				buckets      []madmin.ResyncBucketStatus
				requestError bool
				wantExit     int
			}{
				{name: "success"},
				{
					name:    "success_with_bucket",
					buckets: []madmin.ResyncBucketStatus{{Bucket: "healthy-bucket", Status: "Completed"}},
				},
				{
					name:     "top_only",
					topError: "site resync operation encountered a metadata error",
					wantExit: 1,
				},
				{
					name:     "top_only_failed_status",
					status:   "Failed",
					topError: "site resync operation could not be applied",
					wantExit: 1,
				},
				{
					name: "bucket_only_empty_status",
					buckets: []madmin.ResyncBucketStatus{
						{Bucket: "healthy-bucket", Status: "Completed"},
						{Bucket: "failed-bucket", ErrDetail: "unable to update replication target"},
					},
					wantExit: 1,
				},
				{
					name:     "mixed",
					topError: "site resync operation encountered a metadata error",
					buckets: []madmin.ResyncBucketStatus{
						{Bucket: "healthy-bucket", Status: "Completed"},
						{Bucket: "failed-bucket", ErrDetail: "unable to update replication target"},
						{Bucket: "other-bucket", Status: "Failed", ErrDetail: "unable to save bucket metadata"},
					},
					wantExit: 1,
				},
				{name: "request_error", requestError: true, wantExit: 1},
			} {
				t.Run(op+"/"+mode+"/"+tc.name, func(t *testing.T) {
					status := "Ongoing"
					success := "Site resync started with ID operation-test"
					if op == "cancel" {
						status = "Canceled"
						success = "Site resync with ID operation-test canceled successfully."
					}
					if tc.status != "" {
						status = tc.status
					}
					// SILO returns errorDetail without a bucket status for target
					// configuration errors, including after workers are canceled.
					response, err := json.Marshal(madmin.SRResyncOpStatus{
						OpType:    op,
						ResyncID:  "operation-test",
						Status:    status,
						Buckets:   tc.buckets,
						ErrDetail: tc.topError,
					})
					if err != nil {
						t.Fatal(err)
					}
					server := newResyncOperationTestServer(t, op, response, tc.requestError)
					args := []string{"admin", "replicate", "resync", op, "b4", "peer2"}
					switch mode {
					case "json":
						args = append([]string{"--json"}, args...)
					case "json_leaf_flag":
						args = append(args, "--json")
					}
					result := runChecksumVerifyCLI(t, server.URL, []string{
						"MC_HOST_peer2=http://access:secret@" + strings.TrimPrefix(server.URL, "http://"),
					}, args...)
					if result.exitCode != tc.wantExit {
						t.Errorf("exit=%d, want %d; stdout=%q stderr=%q",
							result.exitCode, tc.wantExit, result.stdout, result.stderr)
					}

					if tc.requestError {
						wantMessage := "Unable to " + op + " replication resync"
						if mode != "text" {
							var got struct {
								Status string       `json:"status"`
								Error  errorMessage `json:"error"`
							}
							if err := json.Unmarshal(result.stdout, &got); err != nil {
								t.Fatalf("expected one JSON error: %v; stdout=%q", err, result.stdout)
							}
							if got.Status != "error" || got.Error.Type != "fatal" || got.Error.Message != wantMessage ||
								got.Error.Cause.Message != "resync mutation denied" {
								t.Errorf("unexpected API error: %+v", got)
							}
							if len(result.stderr) != 0 {
								t.Errorf("unexpected stderr: %q", result.stderr)
							}
						} else if len(result.stdout) != 0 || strings.Count(string(result.stderr), wantMessage) != 1 ||
							strings.Count(string(result.stderr), "resync mutation denied") != 1 {
							t.Errorf("unexpected API error output: stdout=%q stderr=%q", result.stdout, result.stderr)
						}
						return
					}
					if len(result.stderr) != 0 {
						t.Errorf("unexpected stderr: %q", result.stderr)
					}
					if mode != "text" {
						// Compare the complete document, including null buckets,
						// empty status fields and omitted errorDetail fields.
						var got, want any
						if err := json.Unmarshal(result.stdout, &got); err != nil {
							t.Fatalf("expected exactly one JSON response: %v; stdout=%q", err, result.stdout)
						}
						if err := json.Unmarshal(response, &want); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(got, want) {
							t.Errorf("JSON response changed: got %s, want %s", result.stdout, response)
						}
						return
					}
					output := string(result.stdout)
					if tc.wantExit == 0 {
						if output != success+"\n" {
							t.Errorf("stdout=%q, want %q", output, success+"\n")
						}
						return
					}
					if strings.Contains(output, success) {
						t.Errorf("partial failure reported unconditional success: %q", output)
					}
					if op == "cancel" {
						if status == "Canceled" && !strings.Contains(output, "canceled, but cleanup is incomplete") {
							t.Errorf("missing canceled-with-incomplete-cleanup explanation: %q", output)
						}
						if status != "Canceled" && strings.Contains(output, "canceled") {
							t.Errorf("cancellation was not confirmed by the server: %q", output)
						}
					}
					if tc.topError != "" && strings.Count(output, tc.topError) != 1 {
						t.Errorf("top-level error missing or repeated: %q", output)
					}
					for _, bucket := range tc.buckets {
						if bucket.ErrDetail == "" {
							continue
						}
						detail := fmt.Sprintf("Bucket %s: %s", bucket.Bucket, bucket.ErrDetail)
						if strings.Count(output, detail) != 1 {
							t.Errorf("bucket detail %q missing or repeated: %q", detail, output)
						}
					}
				})
			}
		}
	}
}
