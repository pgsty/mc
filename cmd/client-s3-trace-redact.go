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
	"regexp"
)

// redactedMarker replaces every secret this package removes from --debug output.
const redactedMarker = "**REDACTED**"

// Credential=<access-key-id>/<date>/<region>/<service>/aws4_request. The key id
// is everything up to the first separator: SILO and MinIO deployments routinely
// use lowercase keys such as "minioadmin", and STS keys mix case, so this must
// not assume the uppercase alphanumeric shape of an AWS-issued key.
var traceCredentialRegexp = regexp.MustCompile(`Credential=[^/,\s]+/`)

// Signature=<hex>. Also covers the AWS4-HMAC-SHA256 query form.
var traceSignatureRegexp = regexp.MustCompile(`Signature=[0-9a-fA-F]+`)

// Signature v2: "AWS <access-key-id>:<base64 signature>". Anchored so an
// AWS4-HMAC-SHA256 value can never be mistaken for it.
var traceV2AuthRegexp = regexp.MustCompile(`^AWS [^:\s]+:`)

// traceSecretHeaders lists headers whose value is credential material. They are
// replaced wholesale rather than pattern-matched.
var traceSecretHeaders = []string{
	// STS session credentials. Leaking this is equivalent to leaking the
	// secret key for the lifetime of the token.
	"X-Amz-Security-Token",
	// Customer-supplied encryption keys for the destination object...
	"X-Amz-Server-Side-Encryption-Customer-Key",
	// ...and for the source object of a server-side copy.
	"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key",
}

// traceSecretQueryParams lists query parameters that carry credential material
// in presigned requests.
var traceSecretQueryParams = []string{
	"X-Amz-Credential",
	"X-Amz-Signature",
	"X-Amz-Security-Token",
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

	if auth := redacted.Header.Get("Authorization"); auth != "" {
		if traceV2AuthRegexp.MatchString(auth) {
			// Signature v2: "AWS <access-key-id>:<signature>". Nothing in the
			// value is safe to keep, so replace the whole thing.
			auth = "AWS " + redactedMarker + ":" + redactedMarker
		} else {
			auth = traceCredentialRegexp.ReplaceAllString(auth, "Credential="+redactedMarker+"/")
			auth = traceSignatureRegexp.ReplaceAllString(auth, "Signature="+redactedMarker)
		}
		redacted.Header.Set("Authorization", auth)
	}

	for _, header := range traceSecretHeaders {
		if redacted.Header.Get(header) != "" {
			redacted.Header.Set(header, redactedMarker)
		}
	}

	if req.URL != nil {
		clonedURL := *req.URL
		query := clonedURL.Query()
		changed := false
		for _, param := range traceSecretQueryParams {
			if query.Get(param) != "" {
				query.Set(param, redactedMarker)
				changed = true
			}
		}
		if changed {
			clonedURL.RawQuery = query.Encode()
		}
		redacted.URL = &clonedURL
	}

	return &redacted
}
