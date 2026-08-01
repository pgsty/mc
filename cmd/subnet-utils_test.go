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
	"strings"
	"testing"
)

func TestDumpHTTPReqRedactsSecrets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/debug?api-key=&api-key=query-secret&api-key=second-secret&api_key=underscore-secret", nil)
	req.Header.Set("Authorization", "Bearer auth-secret")
	req.Header.Set("x-subnet-license", "license-header-secret")
	req.Header.Set("x-subnet-api-key", "header-api-secret")
	responseBody := `{"api_key":"response-api-secret","license":"response-license-secret"}`
	resp := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Request:    req,
	}
	var trace bytes.Buffer
	if err := dumpHTTPReqTo(&trace, req, resp); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"api-key": strings.Repeat("*", len("second-secret")),
		"api_key": strings.Repeat("*", len("underscore-secret")),
	} {
		values := req.URL.Query()[key]
		if len(values) != 1 || values[0] != want {
			t.Errorf("query parameter %q was not fully redacted: %q", key, values)
		}
	}
	for _, secret := range []string{
		"query-secret",
		"second-secret",
		"underscore-secret",
		"auth-secret",
		"license-header-secret",
		"header-api-secret",
		"response-api-secret",
		"response-license-secret",
	} {
		if strings.Contains(trace.String(), secret) {
			t.Errorf("HTTP trace contains secret %q: %s", secret, trace.String())
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != responseBody {
		t.Fatalf("response body changed while tracing: got %q, want %q", got, responseBody)
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
