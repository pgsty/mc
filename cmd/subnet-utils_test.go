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
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestDumpHTTPReqRedactsSecrets(t *testing.T) {
	const (
		requestURL   = "https://example.test/debug?api-key=&api-key=query-secret&api-key=second-secret&api_key=underscore-secret"
		responseBody = `{"api_key":"response-api-secret","license":"response-license-secret"}`
		cookieOne    = "session=cookie-secret; HttpOnly"
		cookieTwo    = "refresh=second-cookie-secret; Secure"
	)

	tests := []struct {
		name             string
		body             string
		contentLength    int64
		transferEncoding []string
	}{
		{name: "empty", contentLength: 0},
		{name: "fixed length", body: responseBody, contentLength: int64(len(responseBody))},
		{name: "unknown length", body: responseBody, contentLength: -1, transferEncoding: []string{"chunked"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestURL, nil)
			req.Header.Set("Authorization", "Bearer auth-secret")
			req.Header.Set("x-subnet-license", "license-header-secret")
			req.Header.Set("x-subnet-api-key", "header-api-secret")
			originalQuery := req.URL.RawQuery
			originalRequestHeaders := req.Header.Clone()

			resp := &http.Response{
				Status:           "200 OK",
				StatusCode:       http.StatusOK,
				Proto:            "HTTP/1.1",
				ProtoMajor:       1,
				ProtoMinor:       1,
				Header:           make(http.Header),
				Body:             io.NopCloser(strings.NewReader(tt.body)),
				ContentLength:    tt.contentLength,
				TransferEncoding: append([]string(nil), tt.transferEncoding...),
				Request:          req,
			}
			resp.Header.Add("Set-Cookie", cookieOne)
			resp.Header.Add("Set-Cookie", cookieTwo)
			resp.Header.Set("Authorization", "Bearer response-auth-secret")
			resp.Header.Set("x-subnet-license", "response-license-header-secret")
			resp.Header.Set("x-subnet-api-key", "response-api-header-secret")
			resp.Header.Set("x-trace-id", "trace-id")
			originalResponseHeaders := resp.Header.Clone()
			originalTransferEncoding := append([]string(nil), resp.TransferEncoding...)

			var trace bytes.Buffer
			if err := dumpHTTPReqTo(&trace, req, resp); err != nil {
				t.Fatal(err)
			}

			wantQuery := url.Values{
				"api-key": {strings.Repeat("*", len("second-secret"))},
				"api_key": {strings.Repeat("*", len("underscore-secret"))},
			}.Encode()
			for _, want := range []string{
				"GET /debug?" + wantQuery + " HTTP/1.1",
				"HTTP/1.1 200 OK",
				"X-Trace-Id: trace-id",
				"Set-Cookie: " + strings.Repeat("*", len(cookieTwo)),
			} {
				if !strings.Contains(trace.String(), want) {
					t.Errorf("HTTP trace does not contain %q: %s", want, trace.String())
				}
			}

			for _, secret := range []string{
				"query-secret",
				"second-secret",
				"underscore-secret",
				"auth-secret",
				"license-header-secret",
				"header-api-secret",
				"response-auth-secret",
				"response-license-header-secret",
				"response-api-header-secret",
				"response-api-secret",
				"response-license-secret",
				"cookie-secret",
				"second-cookie-secret",
			} {
				if strings.Contains(trace.String(), secret) {
					t.Errorf("HTTP trace contains secret %q: %s", secret, trace.String())
				}
			}

			if req.URL.RawQuery != originalQuery {
				t.Errorf("request query changed while tracing: got %q, want %q", req.URL.RawQuery, originalQuery)
			}
			if !reflect.DeepEqual(req.Header, originalRequestHeaders) {
				t.Errorf("request headers changed while tracing: got %#v, want %#v", req.Header, originalRequestHeaders)
			}
			if !reflect.DeepEqual(resp.Header, originalResponseHeaders) {
				t.Errorf("response headers changed while tracing: got %#v, want %#v", resp.Header, originalResponseHeaders)
			}
			if resp.ContentLength != tt.contentLength {
				t.Errorf("response content length changed while tracing: got %d, want %d", resp.ContentLength, tt.contentLength)
			}
			if !reflect.DeepEqual(resp.TransferEncoding, originalTransferEncoding) {
				t.Errorf("response transfer encoding changed while tracing: got %q, want %q", resp.TransferEncoding, originalTransferEncoding)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(body); got != tt.body {
				t.Fatalf("response body changed while tracing: got %q, want %q", got, tt.body)
			}
		})
	}
}

func TestSubnetBaseURL(t *testing.T) {
	sbu := SubnetBaseURL()
	u, err := url.ParseRequestURI(sbu)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" {
		t.Fatalf("Expected TestSubnetBaseURL() to return an https url, received %s", u.Scheme)
	}
}
