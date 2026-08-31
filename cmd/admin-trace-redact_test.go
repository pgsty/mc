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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
)

// `admin trace` prints header maps the server supplies for other clients'
// requests. Every secret header class must be redacted in both renderings,
// ordinary headers must stay visible, and the event itself must not be
// modified - it is shared between the human and JSON paths.
func TestAdminTraceRedactsServerSuppliedHeaders(t *testing.T) {
	const sentinel = "TRACESENTINEL0123456789"
	reqHeaders := http.Header{
		"Host":                 {"silo.example.com"},
		"Authorization":        {"AWS4-HMAC-SHA256 Credential=" + sentinel + "/20260831/us-east-1/s3/aws4_request, Signature=" + sentinel},
		"X-Amz-Security-Token": {sentinel},
		"X-Amz-Server-Side-Encryption-Customer-Key": {sentinel},
		"Cookie":     {"sid=" + sentinel, "other=" + sentinel},
		"X-Api-Key":  {sentinel},
		"User-Agent": {"keep-user-agent"},
	}
	respHeaders := http.Header{
		"Set-Cookie":   {"sid=" + sentinel},
		"Location":     {"https://user:" + sentinel + "@host/x?X-Amz-Signature=" + sentinel},
		"Content-Type": {"application/xml"},
	}
	msg := traceMessage{
		Status: "success",
		ServiceTraceInfo: madmin.ServiceTraceInfo{
			Trace: madmin.TraceInfo{
				TraceType: madmin.TraceS3,
				NodeName:  "node1",
				FuncName:  "s3.GetObject",
				Time:      time.Unix(100, 0).UTC(),
				Path:      "/bucket/object",
				HTTP: &madmin.TraceHTTPStats{
					ReqInfo: madmin.TraceRequestInfo{
						Time:     time.Unix(100, 0).UTC(),
						Proto:    "HTTP/1.1",
						Method:   http.MethodGet,
						Path:     "/bucket/object",
						RawQuery: "X-Amz-Signature=" + sentinel + "&versionId=keep-version",
						Headers:  reqHeaders,
						Body:     []byte("Authorization: Bearer " + sentinel),
						Client:   "10.0.0.1",
					},
					RespInfo: madmin.TraceResponseInfo{
						Time:       time.Unix(101, 0).UTC(),
						Headers:    respHeaders,
						Body:       []byte("<Error>token " + sentinel + "</Error>"),
						StatusCode: http.StatusForbidden,
					},
				},
			},
		},
	}

	for name, rendered := range map[string]string{"json": msg.JSON(), "human": msg.String()} {
		if strings.Contains(rendered, sentinel) {
			t.Errorf("%s trace output leaked a server-supplied secret:\n%s", name, rendered)
		}
		for _, keep := range []string{"keep-user-agent", "application/xml", "keep-version", "silo.example.com"} {
			if !strings.Contains(rendered, keep) {
				t.Errorf("%s trace output lost an ordinary field %q:\n%s", name, keep, rendered)
			}
		}
	}

	// The event maps are shared with the other rendering and must be intact.
	if got := reqHeaders.Get("Authorization"); !strings.Contains(got, sentinel) {
		t.Fatalf("request header map was mutated: %q", got)
	}
	if got := len(reqHeaders.Values("Cookie")); got != 2 {
		t.Fatalf("request header map lost values: %d", got)
	}
	if _, ok := reqHeaders["Host"]; !ok {
		t.Fatal("Host was deleted from the source event map")
	}
	if got := respHeaders.Get("Set-Cookie"); !strings.Contains(got, sentinel) {
		t.Fatalf("response header map was mutated: %q", got)
	}
}

func TestRedactHeaderMapHandlesEveryValueAndCopies(t *testing.T) {
	source := map[string][]string{
		"Authorization": {"", "Bearer secretvalue0123"},
		"X-Auth-Token":  {"first0123", "second0123"},
		"Accept":        {"*/*"},
	}
	got := redactHeaderMap(source)
	if got["Authorization"][0] != "" || got["Authorization"][1] != "Bearer "+redactedMarker {
		t.Fatalf("Authorization values: %v", got["Authorization"])
	}
	if len(got["X-Auth-Token"]) != 1 || got["X-Auth-Token"][0] != redactedMarker {
		t.Fatalf("token values: %v", got["X-Auth-Token"])
	}
	if got["Accept"][0] != "*/*" {
		t.Fatalf("ordinary header changed: %v", got["Accept"])
	}
	if source["Authorization"][1] != "Bearer secretvalue0123" || source["X-Auth-Token"][1] != "second0123" {
		t.Fatal("source map was mutated")
	}
	if redactHeaderMap(nil) != nil {
		t.Fatal("nil map should stay nil")
	}
}

func TestAdminTraceScrubsKnownQuotedSecretBeforeShapeSweep(t *testing.T) {
	const secret = `json"admin-trace-secret`
	msg := traceMessage{
		Status: "success",
		ServiceTraceInfo: madmin.ServiceTraceInfo{Trace: madmin.TraceInfo{
			TraceType: madmin.TraceS3,
			NodeName:  "node1",
			FuncName:  "s3.GetObject",
			Time:      time.Unix(100, 0).UTC(),
			Path:      "/bucket/object",
			HTTP: &madmin.TraceHTTPStats{
				ReqInfo: madmin.TraceRequestInfo{
					Time:    time.Unix(100, 0).UTC(),
					Proto:   "HTTP/1.1",
					Method:  http.MethodGet,
					Path:    "/bucket/object",
					Headers: http.Header{"X-Auth-Token": {secret}},
					Body:    []byte("AWS4-HMAC-SHA256 Credential=x/20260831/r/s/aws4_request, Signature=abc token=[" + secret + "]"),
				},
				RespInfo: madmin.TraceResponseInfo{Time: time.Unix(101, 0).UTC(), StatusCode: http.StatusForbidden},
			},
		}},
	}
	for name, rendered := range map[string]string{"human": msg.String(), "json": msg.JSON()} {
		if strings.Contains(rendered, "admin-trace-secret") || strings.Contains(rendered, secret) {
			t.Fatalf("%s trace leaked suffix of a known quoted secret:\n%s", name, rendered)
		}
	}
}
