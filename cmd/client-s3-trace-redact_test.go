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
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

const (
	testSSECKey       = "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE="
	testSourceSSECKey = "c291cmNlMzJieXRlc2xvbmdzZWNyZXRrZXlnaXZlbjE="
	testSessionToken  = "FQoGZXIvYXdzEBYaDG9wYXF1ZXRva2VuZXhhbXBsZQ"
)

func newTraceProbeRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://silo.example.com/bucket/object", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// dumpRedacted renders the request the way the tracers do, so the assertions
// cover what actually reaches --debug output rather than the header map alone.
func dumpRedacted(t *testing.T, req *http.Request) string {
	t.Helper()
	dump, err := httputil.DumpRequestOut(redactRequestForTrace(req), false)
	if err != nil {
		t.Fatal(err)
	}
	return string(dump)
}

func TestRedactRequestForTraceHidesLowercaseAccessKey(t *testing.T) {
	// A SILO or MinIO deployment's default key is lowercase; the previous
	// [A-Z0-9]+ pattern let it through verbatim. A key may also contain a
	// slash - this client only checks a key's length, and minio-go builds
	// Credential by concatenating the raw key with the scope - so a redactor
	// that stops at the first "/" would leak the rest of it.
	for _, accessKey := range []string{
		"minioadmin", "AKIAJNACEGBGMXBHLEZA", "siloAdmin1", "key-with.symbols", "team/alice", "a/b/c/d",
	} {
		req := newTraceProbeRequest(t)
		req.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential="+accessKey+"/20260830/us-east-1/s3/aws4_request, "+
				"SignedHeaders=host;x-amz-date, Signature=bbfaa693c626021bcb5f911cd898a1a30206c1fad6bad1e0eb89e282173bd24c")

		dump := dumpRedacted(t, req)
		if strings.Contains(dump, accessKey) {
			t.Errorf("access key %q leaked into trace: %s", accessKey, dump)
		}
		if strings.Contains(dump, "bbfaa693c626021bcb5f911cd898a1a30206c1fad6bad1e0eb89e282173bd24c") {
			t.Errorf("signature leaked into trace: %s", dump)
		}
		if !strings.Contains(dump, "Credential="+redactedMarker+"/20260830/us-east-1/s3/aws4_request") {
			t.Errorf("credential scope should survive redaction: %s", dump)
		}
	}
}

func TestRedactRequestForTraceHidesSignatureV2Authorization(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.Header.Set("Authorization", "AWS minioadmin:Y10YHUZ0DTUterAUI6w3XKX7Iqk=")

	dump := dumpRedacted(t, req)
	if strings.Contains(dump, "minioadmin") || strings.Contains(dump, "Y10YHUZ0DTUterAUI6w3XKX7Iqk=") {
		t.Fatalf("signature v2 credentials leaked into trace: %s", dump)
	}
	if !strings.Contains(dump, "AWS "+redactedMarker+":"+redactedMarker) {
		t.Fatalf("signature v2 header not redacted: %s", dump)
	}
}

func TestRedactRequestForTraceHidesSecretHeaders(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.Header.Set("X-Amz-Security-Token", testSessionToken)
	req.Header.Set("X-Amz-Server-Side-Encryption-Customer-Key", testSSECKey)
	req.Header.Set("X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key", testSourceSSECKey)

	dump := dumpRedacted(t, req)
	for name, secret := range map[string]string{
		"security token":  testSessionToken,
		"SSE-C key":       testSSECKey,
		"copy-source key": testSourceSSECKey,
	} {
		if strings.Contains(dump, secret) {
			t.Errorf("%s leaked into trace: %s", name, dump)
		}
	}
}

func TestRedactRequestForTraceHidesPresignedQueryCredentials(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.URL.RawQuery = url.Values{
		"X-Amz-Credential":     []string{"minioadmin/20260830/us-east-1/s3/aws4_request"},
		"X-Amz-Signature":      []string{"bbfaa693c626021bcb5f911cd898a1a3"},
		"X-Amz-SecurityToken":  []string{"unrelated"},
		"X-Amz-Security-Token": []string{testSessionToken},
		"versionId":            []string{"keep-me"},
	}.Encode()

	dump := dumpRedacted(t, req)
	for _, secret := range []string{"minioadmin", "bbfaa693c626021bcb5f911cd898a1a3", testSessionToken} {
		if strings.Contains(dump, secret) {
			t.Errorf("presigned credential %q leaked into trace: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "versionId=keep-me") {
		t.Errorf("non-secret query parameters must survive: %s", dump)
	}
}

// The tracer runs inside an http.RoundTripper, which must not modify the
// request it was handed. Redacting in place also risked sending "**REDACTED**"
// as the SSE-C key if the request were retried.
func TestRedactRequestForTraceLeavesCallerRequestIntact(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=minioadmin/20260830/us-east-1/s3/aws4_request, Signature=abc123")
	req.Header.Set("X-Amz-Server-Side-Encryption-Customer-Key", testSSECKey)
	req.Header.Set("X-Amz-Security-Token", testSessionToken)
	originalQuery := "versionId=keep-me&X-Amz-Signature=abc123"
	req.URL.RawQuery = originalQuery
	originalURL := req.URL

	dumpRedacted(t, req)

	if got := req.Header.Get("X-Amz-Server-Side-Encryption-Customer-Key"); got != testSSECKey {
		t.Errorf("SSE-C key was mutated on the caller's request: %q", got)
	}
	if got := req.Header.Get("X-Amz-Security-Token"); got != testSessionToken {
		t.Errorf("security token was mutated on the caller's request: %q", got)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "minioadmin") {
		t.Errorf("Authorization was mutated on the caller's request: %q", req.Header.Get("Authorization"))
	}
	if req.URL != originalURL || req.URL.RawQuery != originalQuery {
		t.Errorf("URL was mutated on the caller's request: %q", req.URL.RawQuery)
	}
}

func TestRedactRequestForTraceHandlesRequestWithoutCredentials(t *testing.T) {
	req := newTraceProbeRequest(t)
	dump := dumpRedacted(t, req)
	if !strings.Contains(dump, "GET /bucket/object HTTP/1.1") {
		t.Fatalf("unauthenticated request should still render: %s", dump)
	}
}

func TestRedactRequestForTraceHidesUnparseableCredential(t *testing.T) {
	// No 8-digit scope date to anchor on: give up the scope rather than the key.
	req := newTraceProbeRequest(t)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=minioadmin/not-a-scope, Signature=abc123")

	dump := dumpRedacted(t, req)
	if strings.Contains(dump, "minioadmin") {
		t.Fatalf("access key leaked when the scope did not parse: %s", dump)
	}
}

// --custom-header lets a caller attach any Authorization value, and a proxy in
// front of the endpoint may want Basic or Bearer credentials.
func TestRedactRequestForTraceHidesNonS3AuthorizationSchemes(t *testing.T) {
	for name, value := range map[string]string{
		"basic":   "Basic dXNlcjpodW50ZXIy",
		"bearer":  "Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
		"opaque":  "some-opaque-token-value",
		"negotia": "Negotiate YIIFm",
	} {
		t.Run(name, func(t *testing.T) {
			req := newTraceProbeRequest(t)
			req.Header.Set("Authorization", value)

			dump := dumpRedacted(t, req)
			secret := value
			if scheme, rest, ok := strings.Cut(value, " "); ok {
				secret = rest
				if !strings.Contains(dump, scheme) {
					t.Errorf("scheme %q should stay visible: %s", scheme, dump)
				}
			}
			if strings.Contains(dump, secret) {
				t.Fatalf("credential leaked for %s: %s", name, dump)
			}
		})
	}
}

// minio-go sends this instead of X-Amz-Security-Token for S3 Express buckets.
func TestRedactRequestForTraceHidesS3ExpressSessionToken(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.Header.Set("X-Amz-S3session-Token", testSessionToken)

	if dump := dumpRedacted(t, req); strings.Contains(dump, testSessionToken) {
		t.Fatalf("S3 Express session token leaked into trace: %s", dump)
	}
}

func TestRedactRequestForTraceHidesSignatureV2QueryCredentials(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.URL.RawQuery = url.Values{
		"AWSAccessKeyId": []string{"minioadmin"},
		"Signature":      []string{"Y10YHUZ0DTUterAUI6w3XKX7Iqk="},
		"Expires":        []string{"1787765892"},
	}.Encode()

	dump := dumpRedacted(t, req)
	for _, secret := range []string{"minioadmin", "Y10YHUZ0DTUterAUI6w3XKX7Iqk="} {
		if strings.Contains(dump, secret) {
			t.Errorf("signature v2 presign credential %q leaked: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "Expires=1787765892") {
		t.Errorf("non-secret query parameters must survive: %s", dump)
	}
}

// url.Values.Get returns the first value, so a repeated parameter whose first
// value is empty could hide a real one behind it.
func TestRedactRequestForTraceHidesRepeatedQueryCredential(t *testing.T) {
	req := newTraceProbeRequest(t)
	req.URL.RawQuery = "X-Amz-Signature=&X-Amz-Signature=bbfaa693c626021bcb5f911cd898a1a3"

	if dump := dumpRedacted(t, req); strings.Contains(dump, "bbfaa693c626021bcb5f911cd898a1a3") {
		t.Fatalf("repeated query credential leaked: %s", dump)
	}
}

func dumpResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	dump, err := dumpResponseForTrace(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(dump)
}

func newTraceProbeResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	req := newTraceProbeRequest(t)
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func TestDumpResponseForTraceHidesSecretResponseHeaders(t *testing.T) {
	resp := newTraceProbeResponse(t, http.StatusBadRequest, "<Error/>")
	resp.Header.Set("X-Amz-Security-Token", testSessionToken)
	resp.Header.Set("X-Amz-Server-Side-Encryption-Customer-Key", testSSECKey)
	resp.Header.Set("Location", "https://silo.example.com/b/o?X-Amz-Signature=abc123&versionId=keep")

	dump := dumpResponse(t, resp)
	for _, secret := range []string{testSessionToken, testSSECKey, "X-Amz-Signature=abc123"} {
		if strings.Contains(dump, secret) {
			t.Errorf("response secret %q leaked: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "versionId=keep") {
		t.Errorf("non-secret redirect parameters must survive: %s", dump)
	}
}

// A proxying or buggy endpoint can echo a request header into its error text.
func TestDumpResponseForTraceScrubsReflectedRequestSecrets(t *testing.T) {
	resp := newTraceProbeResponse(t, http.StatusBadRequest,
		"<Error><Message>bad key "+testSSECKey+" for token "+testSessionToken+"</Message></Error>")
	resp.Request.Header.Set("X-Amz-Server-Side-Encryption-Customer-Key", testSSECKey)
	resp.Request.Header.Set("X-Amz-Security-Token", testSessionToken)

	dump := dumpResponse(t, resp)
	for _, secret := range []string{testSSECKey, testSessionToken} {
		if strings.Contains(dump, secret) {
			t.Errorf("reflected request secret %q leaked through the error body: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "<Error><Message>bad key") {
		t.Errorf("the rest of the error body must survive: %s", dump)
	}
}

// httputil.DumpResponse restores the body on whatever value it is handed, so
// the drain has to happen on the caller's response, not on a copy.
func TestDumpResponseForTraceLeavesBodyReadable(t *testing.T) {
	const body = "<Error><Code>NoSuchKey</Code></Error>"
	resp := newTraceProbeResponse(t, http.StatusNotFound, body)

	if dump := dumpResponse(t, resp); !strings.Contains(dump, "NoSuchKey") {
		t.Fatalf("error body should be dumped: %s", dump)
	}
	remaining, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(remaining) != body {
		t.Fatalf("caller's response body was consumed: %q", string(remaining))
	}
}

// A 2xx body can be an object payload; only failures are worth dumping.
func TestDumpResponseForTraceOmitsSuccessBodies(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusPartialContent} {
		resp := newTraceProbeResponse(t, status, "SENSITIVE-OBJECT-PAYLOAD")
		if dump := dumpResponse(t, resp); strings.Contains(dump, "SENSITIVE-OBJECT-PAYLOAD") {
			t.Errorf("status %d should not dump its body: %s", status, dump)
		}
	}
}
