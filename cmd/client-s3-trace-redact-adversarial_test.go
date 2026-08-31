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

// Adversarial cases for credential redaction. Each one is a shape that the
// first, pattern-matching redactor let through. The rule they enforce is
// fail-closed: a value the redactor cannot parse is withheld whole, never
// passed on in the hope that it holds nothing sensitive.

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

const adversarialSecret = "SUPERSECRETVALUE0123456789"

func adversarialRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://silo.example.com/bucket/object", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func adversarialDump(t *testing.T, req *http.Request) string {
	t.Helper()
	dump, err := httputil.DumpRequestOut(redactRequestForTrace(req), false)
	if err != nil {
		t.Fatal(err)
	}
	return string(dump)
}

func adversarialResponse(t *testing.T, header http.Header, body string) *http.Response {
	t.Helper()
	req := adversarialRequest(t)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=deadbeef")
	return &http.Response{
		Status: "400 Bad Request", StatusCode: http.StatusBadRequest,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: header, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: req,
	}
}

func TestAdversarialRequestRedaction(t *testing.T) {
	for name, build := range map[string]func(*http.Request){
		"duplicate Authorization, first value empty": func(r *http.Request) {
			r.Header.Add("Authorization", "")
			r.Header.Add("Authorization", "AWS4-HMAC-SHA256 Credential="+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, Signature=abc")
		},
		"duplicate Authorization, both real": func(r *http.Request) {
			r.Header.Add("Authorization", "Bearer "+adversarialSecret)
			r.Header.Add("Authorization", "AWS4-HMAC-SHA256 Credential="+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, Signature=abc")
		},
		"access key containing a space": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key "+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc")
		},
		"access key containing a comma": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key,"+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc")
		},
		"access key containing a date fragment": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=abc/20200101/"+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc")
		},
		"access key containing a fake scope": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=x/20200101/r/s/aws4_request/"+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, Signature=abc")
		},
		"malformed SigV4 without a scope": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key "+adversarialSecret)
		},
		"SigV2 with a colon in the key": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS a:"+adversarialSecret+":sig")
		},
		"Proxy-Authorization": func(r *http.Request) {
			r.Header.Set("Proxy-Authorization", "Basic "+adversarialSecret)
		},
		"Cookie": func(r *http.Request) {
			r.Header.Set("Cookie", "session="+adversarialSecret)
		},
		"custom token header": func(r *http.Request) {
			r.Header.Set("X-Auth-Token", adversarialSecret)
		},
		"custom password header": func(r *http.Request) {
			r.Header.Set("X-Proxy-Password", adversarialSecret)
		},
		"SSE-KMS context": func(r *http.Request) {
			r.Header.Set("X-Amz-Server-Side-Encryption-Context", adversarialSecret)
		},
		"userinfo in the request URL": func(r *http.Request) {
			r.URL.User = url.UserPassword("user", adversarialSecret)
		},
		"custom token query parameter": func(r *http.Request) {
			r.URL.RawQuery = "x-session-token=" + adversarialSecret
		},
		"valid scope, non-hex Signature": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature="+adversarialSecret)
		},
		"unknown trailing field": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=ab12, Extra="+adversarialSecret)
		},
		"unknown field whose name is the secret": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key/20260831/us-east-1/s3/aws4_request, "+adversarialSecret+"=1, Signature=ab12")
		},
		"secret smuggled in SignedHeaders": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=key/20260831/us-east-1/s3/aws4_request, SignedHeaders=host;"+adversarialSecret+", Signature=ab12")
		},
		"text before Credential=": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS4-HMAC-SHA256 "+adversarialSecret+" Credential=key/20260831/us-east-1/s3/aws4_request, Signature=ab12")
		},
		"X-Api-Key": func(r *http.Request) {
			r.Header.Set("X-Api-Key", adversarialSecret)
		},
		"api_key query parameter": func(r *http.Request) {
			r.URL.RawQuery = "api_key=" + adversarialSecret + "&versionId=keep"
		},
		"password query parameter": func(r *http.Request) {
			r.URL.RawQuery = "password=" + adversarialSecret
		},
		"request trailer": func(r *http.Request) {
			r.Trailer = http.Header{"X-Auth-Token": {adversarialSecret}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := adversarialRequest(t)
			build(req)
			if dump := adversarialDump(t, req); strings.Contains(dump, adversarialSecret) {
				t.Fatalf("secret leaked:\n%s", dump)
			}
		})
	}
}

func TestAdversarialResponseRedaction(t *testing.T) {
	for name, resp := range map[string]*http.Response{
		"server reflects Authorization into body": adversarialResponse(t, http.Header{},
			"<Error><Message>got Authorization: AWS4-HMAC-SHA256 Credential="+adversarialSecret+"/20260831/us-east-1/s3/aws4_request, Signature=deadbeef</Message></Error>"),
		"server reflects only the access key": adversarialResponse(t, http.Header{},
			"<Error><Message>unknown key "+adversarialSecret+"</Message></Error>"),
		"server reflects a SigV2 header": adversarialResponse(t, http.Header{},
			"<Error><Message>AWS "+adversarialSecret+":YXNkZmFzZGZhc2RmYXNkZmFzZGY=</Message></Error>"),
		"server reflects a presigned URL": adversarialResponse(t, http.Header{},
			"<Error><Message>https://h/b/o?X-Amz-Signature="+adversarialSecret+"&x=1</Message></Error>"),
		"Set-Cookie":                   adversarialResponse(t, http.Header{"Set-Cookie": {"sid=" + adversarialSecret}}, ""),
		"Location with userinfo":       adversarialResponse(t, http.Header{"Location": {"https://user:" + adversarialSecret + "@host/x"}}, ""),
		"Location that fails to parse": adversarialResponse(t, http.Header{"Location": {"http://[::1/x?X-Amz-Signature=" + adversarialSecret}}, ""),
		"Authorization echoed as a response header": adversarialResponse(t,
			http.Header{"Authorization": {"AWS4-HMAC-SHA256 Credential=" + adversarialSecret + "/20260831/us-east-1/s3/aws4_request"}}, ""),
		"server reflects a non-hex signature": adversarialResponse(t, http.Header{},
			"<Error><Message>Signature="+adversarialSecret+" was rejected</Message></Error>"),
		"server reflects a password query": adversarialResponse(t, http.Header{},
			"<Error><Message>see https://h/x?password="+adversarialSecret+"&api_key="+adversarialSecret+"</Message></Error>"),
		"server reflects only the Bearer payload": bearerReflectingResponse(t, "<Error><Message>bad token "+adversarialSecret+"</Message></Error>"),
		"server reflects the Basic password":      basicReflectingResponse(t, "<Error><Message>wrong password "+adversarialSecret+"</Message></Error>"),
		"response trailer":                        trailerResponse(t),
	} {
		t.Run(name, func(t *testing.T) {
			dump, err := dumpResponseForTrace(resp)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(dump), adversarialSecret) {
				t.Fatalf("secret leaked:\n%s", string(dump))
			}
		})
	}
}

// Nothing of a signed value survives except its scheme: every preserved field
// - a scope, a SignedHeaders list - is a place a secret can be put.
func TestRedactAuthorizationKeepsOnlyTheScheme(t *testing.T) {
	for value, want := range map[string]string{
		"AWS4-HMAC-SHA256 Credential=k/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=ab12":                                            "AWS4-HMAC-SHA256 " + redactedMarker,
		"AWS4-HMAC-SHA256 Credential=k/20260831/" + strings.ToLower(adversarialSecret) + "/s3/aws4_request, Signature=ab12":                               "AWS4-HMAC-SHA256 " + redactedMarker,
		"AWS4-HMAC-SHA256 Credential=k/20260831/us-east-1/s3/aws4_request, SignedHeaders=host;" + strings.ToLower(adversarialSecret) + ", Signature=ab12": "AWS4-HMAC-SHA256 " + redactedMarker,
		"AWS4-HMAC-SHA256 Credential=nope": "AWS4-HMAC-SHA256 " + redactedMarker,
		"AWS key:sig":                      "AWS " + redactedMarker,
		"Bearer " + adversarialSecret:      "Bearer " + redactedMarker,
		"Basic dXNlcjpwYXNz":               "Basic " + redactedMarker,
		"opaque":                           redactedMarker,
		"":                                 "",
	} {
		if got := redactAuthorization(value); got != want {
			t.Errorf("redactAuthorization(%q) = %q, want %q", value, got, want)
		}
	}
}

// A header the caller attached with --custom-header is a secret whatever it
// is called.
func TestArbitraryCustomHeaderIsRedacted(t *testing.T) {
	saved := globalCustomHeader
	globalCustomHeader = http.Header{}
	globalCustomHeader.Add("X-Whatever", adversarialSecret)
	t.Cleanup(func() { globalCustomHeader = saved })

	req := adversarialRequest(t)
	req.Header.Set("X-Whatever", adversarialSecret)
	req.Header.Set("X-Request-Id", "keep-this")
	dump := adversarialDump(t, req)
	if strings.Contains(dump, adversarialSecret) {
		t.Fatalf("custom header value leaked:\n%s", dump)
	}
	if !strings.Contains(dump, "X-Request-Id: keep-this") {
		t.Fatalf("a header the caller did not supply must stay visible:\n%s", dump)
	}

	resp := adversarialResponse(t, http.Header{}, "<Error><Message>bad "+adversarialSecret+"</Message></Error>")
	resp.Request.Header.Set("X-Whatever", adversarialSecret)
	out, err := dumpResponseForTrace(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), adversarialSecret) {
		t.Fatalf("reflected custom header value leaked:\n%s", out)
	}
}

func TestScrubCredentialTextCoversEchoedShapes(t *testing.T) {
	text := "a Credential=" + adversarialSecret + "/20260831/us-east-1/s3/aws4_request b Signature=deadbeef01 c AWS " + adversarialSecret + ":YXNkZmFzZGZhc2RmYXNkZmFzZGY= d ?X-Amz-Security-Token=" + adversarialSecret + "&e=1"
	if got := scrubCredentialText(text); strings.Contains(got, adversarialSecret) || strings.Contains(got, "deadbeef01") {
		t.Fatalf("credential shape survived: %s", got)
	}
}

func bearerReflectingResponse(t *testing.T, body string) *http.Response {
	t.Helper()
	resp := adversarialResponse(t, http.Header{}, body)
	resp.Request.Header.Set("Authorization", "Bearer "+adversarialSecret)
	return resp
}

func basicReflectingResponse(t *testing.T, body string) *http.Response {
	t.Helper()
	resp := adversarialResponse(t, http.Header{}, body)
	resp.Request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+adversarialSecret)))
	return resp
}

func trailerResponse(t *testing.T) *http.Response {
	t.Helper()
	resp := adversarialResponse(t, http.Header{"Trailer": {"X-Auth-Token"}}, "chunk")
	resp.TransferEncoding = []string{"chunked"}
	resp.ContentLength = -1
	resp.Trailer = http.Header{"X-Auth-Token": {adversarialSecret}}
	return resp
}
