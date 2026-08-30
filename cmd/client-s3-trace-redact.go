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
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

// redactedMarker replaces every secret this package removes from --debug output.
const redactedMarker = "**REDACTED**"

// Signature v4 credential: "Credential=<access-key-id>/<yyyymmdd>/<region>/<service>/aws4_request".
//
// The key is NOT delimited by the first "/": minio-go builds this by
// concatenating the raw access key with the scope, and this client only
// validates a key's length, so a key may legitimately contain slashes. Anchor
// on the 8-digit scope date instead, which lets the scope stay readable.
var traceCredentialScopeRegexp = regexp.MustCompile(`Credential=[^,\s]*?/(\d{8}/)`)

// Fallback for any Credential= whose scope does not parse: take the whole
// value. Losing the scope from a debug line beats leaking half a key.
var traceCredentialAnyRegexp = regexp.MustCompile(`Credential=[^,\s]+`)

// Signature=<hex>, in both the Authorization header and the query string.
var traceSignatureRegexp = regexp.MustCompile(`Signature=[0-9a-fA-F]+`)

// Signature v2: "AWS <access-key-id>:<base64 signature>". Anchored so an
// AWS4-HMAC-SHA256 value can never be mistaken for it.
var traceV2AuthRegexp = regexp.MustCompile(`^AWS [^:\s]+:`)

// Recognized signature v4 prefix, e.g. "AWS4-HMAC-SHA256 ".
var traceV4AuthRegexp = regexp.MustCompile(`^AWS4-[A-Z0-9-]+ `)

// Leading scheme token of an Authorization value, used to keep "Basic" or
// "Bearer" visible while discarding everything after it.
var traceAuthSchemeRegexp = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+`)

// traceSecretHeaders lists headers whose value is credential material. They are
// replaced wholesale rather than pattern-matched. Names are matched
// case-insensitively by http.Header.
var traceSecretHeaders = []string{
	// STS session credentials. Leaking this is equivalent to leaking the
	// secret key for the lifetime of the token.
	"X-Amz-Security-Token",
	// The S3 Express equivalent, set by minio-go for zonal buckets.
	"X-Amz-S3session-Token",
	// Customer-supplied encryption keys for the destination object...
	"X-Amz-Server-Side-Encryption-Customer-Key",
	// ...and for the source object of a server-side copy.
	"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key",
}

// traceSecretQueryParams lists query parameters that carry credential material
// in presigned requests, for both signature versions.
var traceSecretQueryParams = []string{
	"X-Amz-Credential",
	"X-Amz-Signature",
	"X-Amz-Security-Token",
	"X-Amz-S3session-Token",
	// Signature v2 presign.
	"AWSAccessKeyId",
	"Signature",
}

// redactAuthorization removes credential material from an Authorization value.
//
// Anything this function does not recognize is replaced down to its scheme
// token, because --custom-header lets a caller attach an arbitrary
// Authorization header, including Basic and Bearer.
func redactAuthorization(value string) string {
	switch {
	case traceV2AuthRegexp.MatchString(value):
		// Signature v2: "AWS <access-key-id>:<signature>". Nothing in the
		// value is safe to keep.
		return "AWS " + redactedMarker + ":" + redactedMarker
	case traceV4AuthRegexp.MatchString(value):
		var redacted string
		if traceCredentialScopeRegexp.MatchString(value) {
			redacted = traceCredentialScopeRegexp.ReplaceAllString(value, "Credential="+redactedMarker+"/$1")
		} else {
			// Only when the scope did not parse; running both would swallow
			// the scope this just took care to preserve.
			redacted = traceCredentialAnyRegexp.ReplaceAllString(value, "Credential="+redactedMarker)
		}
		return traceSignatureRegexp.ReplaceAllString(redacted, "Signature="+redactedMarker)
	default:
		if scheme := traceAuthSchemeRegexp.FindString(value); scheme != "" && scheme != value {
			return scheme + " " + redactedMarker
		}
		return redactedMarker
	}
}

// redactHeader removes credential material from a cloned header in place.
func redactHeader(header http.Header) {
	if auth := header.Get("Authorization"); auth != "" {
		header.Set("Authorization", redactAuthorization(auth))
	}
	for _, name := range traceSecretHeaders {
		// Values(), not Get(): a repeated header whose first value is empty
		// must not hide a later one.
		if len(header.Values(name)) > 0 {
			header.Set(name, redactedMarker)
		}
	}
	// A redirect can hand back a presigned URL carrying a signature.
	if location := header.Get("Location"); location != "" {
		if parsed, err := url.Parse(location); err == nil {
			if redactURLQuery(parsed) {
				header.Set("Location", parsed.String())
			}
		}
	}
}

// redactURLQuery rewrites credential-bearing query parameters in place and
// reports whether anything changed.
func redactURLQuery(u *url.URL) bool {
	if u == nil || u.RawQuery == "" {
		return false
	}
	query := u.Query()
	changed := false
	for _, param := range traceSecretQueryParams {
		if _, ok := query[param]; ok {
			query.Set(param, redactedMarker)
			changed = true
		}
	}
	if changed {
		u.RawQuery = query.Encode()
	}
	return changed
}

// redactRequestForTrace returns a copy of req with every credential removed.
//
// The caller's request is never touched. A tracer runs inside an
// http.RoundTripper, which must not modify the request it is given, and
// httputil.DumpRequestOut swaps req.Body while it renders, so redacting in
// place would both violate that contract and risk sending a redacted SSE-C key
// if the request were ever retried.
func redactRequestForTrace(req *http.Request) *http.Request {
	redacted := *req
	redacted.Header = req.Header.Clone()
	if redacted.Header == nil {
		redacted.Header = http.Header{}
	}
	redactHeader(redacted.Header)

	if req.URL != nil {
		clonedURL := *req.URL
		redactURLQuery(&clonedURL)
		redacted.URL = &clonedURL
	}

	return &redacted
}

// requestSecretValues returns the literal secret strings a request carried, so
// they can be scrubbed from server-controlled text such as an error body that
// echoed a header back.
func requestSecretValues(req *http.Request) []string {
	if req == nil {
		return nil
	}
	var secrets []string
	for _, name := range traceSecretHeaders {
		secrets = append(secrets, req.Header.Values(name)...)
	}
	if req.URL != nil && req.URL.RawQuery != "" {
		query := req.URL.Query()
		for _, param := range traceSecretQueryParams {
			secrets = append(secrets, query[param]...)
		}
	}
	return secrets
}

// dumpResponseForTrace renders resp for --debug with credentials removed.
//
// The body is dumped only for failures: a 2xx carries no diagnostic value and
// its body can be an object payload. resp.Body is drained and restored here
// rather than by httputil.DumpResponse, because DumpResponse restores the body
// on whatever value it was handed - handing it a copy would leave the caller
// holding a consumed reader.
func dumpResponseForTrace(resp *http.Response) ([]byte, error) {
	withBody := resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices

	var body []byte
	if withBody && resp.Body != nil {
		var err error
		if body, err = io.ReadAll(resp.Body); err != nil {
			return nil, err
		}
		if err = resp.Body.Close(); err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}

	redacted := *resp
	redacted.Header = resp.Header.Clone()
	if redacted.Header == nil {
		redacted.Header = http.Header{}
	}
	redactHeader(redacted.Header)
	if withBody {
		redacted.Body = io.NopCloser(bytes.NewReader(body))
	}

	dump, err := httputil.DumpResponse(&redacted, withBody)
	if err != nil {
		return nil, err
	}

	// Last line of defense: an endpoint that echoes a request header into its
	// error message would otherwise print the real value. This cannot cover a
	// secret the server transformed or re-encoded.
	return []byte(redactSecretValues(string(dump), requestSecretValues(resp.Request))), nil
}

// redactSecretValues replaces literal secret strings in text this client did
// not construct.
func redactSecretValues(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" || secret == redactedMarker {
			continue
		}
		text = strings.ReplaceAll(text, secret, redactedMarker)
	}
	return text
}
