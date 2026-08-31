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

// Credential redaction for --debug traces and for admin trace output.
//
// Three rules, all fail-closed:
//
//  1. An Authorization-style value keeps its scheme token and nothing else.
//     No field of a signed value is preserved - not the credential scope, not
//     SignedHeaders - because every preserved field is a place a secret can
//     be put.
//  2. A header is treated as a secret when its name looks like one, or when
//     the caller supplied it with --custom-header. A custom header is where a
//     proxy token or a WAF key goes, whatever the header is called.
//  3. Text this client did not compose - a response body, a server message,
//     a trace annotation - is swept for credential shapes and for the literal
//     values the process has learned.

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

// redactedMarker replaces every secret this package removes from output.
const redactedMarker = "**REDACTED**"

// Leading scheme token of an Authorization value: "AWS4-HMAC-SHA256", "AWS",
// "Basic", "Bearer", ...
var traceAuthSchemeRegexp = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+`)

// Credential shapes in free text. Each replacement keeps at most the token
// that identifies the shape and withholds the rest.
var traceTextShapes = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	// A whole Signature v4 value echoed into text.
	{regexp.MustCompile(`\bAWS4-HMAC-SHA256\b[^\n"'<>]*`), "AWS4-HMAC-SHA256 " + redactedMarker},
	// Its fields on their own.
	{regexp.MustCompile(`\bCredential=[^,\s"'<>]+`), "Credential=" + redactedMarker},
	{regexp.MustCompile(`\bSignedHeaders=[^,\s"'<>]+`), "SignedHeaders=" + redactedMarker},
	{regexp.MustCompile(`\bSignature=[^,\s"'<>&]+`), "Signature=" + redactedMarker},
	// Signature v2: "AWS <access-key-id>:<signature>".
	{regexp.MustCompile(`\bAWS [^\s"'<>]+`), "AWS " + redactedMarker},
	// Scheme-prefixed tokens.
	{regexp.MustCompile(`\b((?i:Bearer|Basic|Digest|Negotiate|NTLM|Token)) [^\s"'<>,]+`), "$1 " + redactedMarker},
	// Credential-bearing query parameters wherever a URL appears, matched by
	// name fragment so a parameter this client never sends is still caught.
	{regexp.MustCompile(`([?&][^=&\s"'<>]*(?i:token|signature|credential|secret|password|api[-_]?key|auth|sig|key)[^=&\s"'<>]*=)[^&\s"'<>]+`), "${1}" + redactedMarker},
}

// isSecretHeaderName reports whether a header's value is credential material.
// It matches by fragment rather than by exact name so a custom header such as
// X-Auth-Token, X-Api-Key or X-Proxy-Password is caught without being listed.
// A few harmless headers match too (a key ID, a checksum of a key); withholding
// those costs nothing.
func isSecretHeaderName(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-Amz-Security-Token", "X-Amz-S3session-Token",
		"X-Amz-Server-Side-Encryption-Customer-Key",
		"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key",
		"X-Amz-Server-Side-Encryption-Context":
		return true
	}
	lower := strings.ToLower(name)
	for _, fragment := range []string{"authorization", "cookie", "token", "secret", "password", "credential", "key", "auth", "encryption-context"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// isCustomHeaderName reports whether the caller attached this header with
// --custom-header. Every such value is treated as a secret regardless of the
// header's name.
func isCustomHeaderName(name string) bool {
	if len(globalCustomHeader) == 0 {
		return false
	}
	_, ok := globalCustomHeader[http.CanonicalHeaderKey(name)]
	return ok
}

// isSecretQueryParam reports whether a query parameter carries credential
// material, for presigned URLs of either signature version and for anything a
// proxy in front of the endpoint might expect.
func isSecretQueryParam(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range []string{"token", "signature", "credential", "secret", "password", "api-key", "apikey", "api_key", "auth", "sig", "awsaccesskeyid", "key"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// redactAuthorization keeps the scheme token of an Authorization-style value
// and withholds everything after it.
func redactAuthorization(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	scheme := traceAuthSchemeRegexp.FindString(value)
	if scheme == "" || scheme == value {
		return redactedMarker
	}
	return scheme + " " + redactedMarker
}

// redactHeaderValues returns the redacted form of one header's values.
func redactHeaderValues(name string, values []string) []string {
	if !isSecretHeaderName(name) && !isCustomHeaderName(name) {
		return values
	}
	switch http.CanonicalHeaderKey(name) {
	case "Authorization", "Proxy-Authorization":
		redacted := make([]string, 0, len(values))
		for _, value := range values {
			redacted = append(redacted, redactAuthorization(value))
		}
		return redacted
	}
	return []string{redactedMarker}
}

// redactHeader removes credential material from a cloned header map in
// place. Every value of a header is handled, never only the first.
func redactHeader(header http.Header) {
	for name, values := range header {
		header[name] = redactHeaderValues(name, values)
	}
	// A redirect target can carry a presigned signature or userinfo.
	if len(header.Values("Location")) > 0 {
		header.Set("Location", redactURLString(header.Get("Location")))
	}
}

// redactHeaderMap is redactHeader for a header map that is not an
// http.Header, such as the maps a server sends in an admin trace event. The
// source map is not touched; a redacted copy is returned.
func redactHeaderMap(header map[string][]string) map[string][]string {
	if header == nil {
		return nil
	}
	redacted := make(map[string][]string, len(header))
	for name, values := range header {
		out := redactHeaderValues(name, values)
		if http.CanonicalHeaderKey(name) == "Location" {
			out = make([]string, 0, len(values))
			for _, value := range values {
				out = append(out, redactURLString(value))
			}
		}
		redacted[name] = append([]string(nil), out...)
	}
	return redacted
}

// redactURL rewrites credential material in a URL in place. Userinfo is
// withheld entirely: a username may itself be an access key.
func redactURL(u *url.URL) {
	if u == nil {
		return
	}
	if u.User != nil {
		u.User = url.User(redactedMarker)
	}
	if u.RawQuery == "" {
		return
	}
	query := u.Query()
	changed := false
	for name := range query {
		if isSecretQueryParam(name) {
			query.Set(name, redactedMarker)
			changed = true
		}
	}
	if changed {
		u.RawQuery = query.Encode()
	}
}

// redactURLString redacts a URL held as text. A value that does not parse
// cannot be inspected, so it is withheld whole.
func redactURLString(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return redactedMarker
	}
	redactURL(parsed)
	// url.Userinfo escapes the marker's asterisks; put them back so the
	// output reads like every other redaction.
	return strings.ReplaceAll(parsed.String(), url.PathEscape(redactedMarker), redactedMarker)
}

// scrubCredentialText removes credential shapes from text this client did not
// construct - a response body, a server error message, a trace annotation.
// It is pattern based, so it also catches an Authorization header that an
// endpoint echoed back with different spacing or framing.
func scrubCredentialText(text string) string {
	for _, shape := range traceTextShapes {
		text = shape.pattern.ReplaceAllString(text, shape.replacement)
	}
	return text
}

// authorizationSecretValues returns the pieces of an Authorization-style value
// a server might echo on their own: the whole value, the payload after the
// scheme, a Basic payload decoded, and for a signed value its credential and
// signature fields.
func authorizationSecretValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	secrets := []string{value}
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		field = strings.TrimPrefix(field, "AWS4-HMAC-SHA256 ")
		if name, fieldValue, ok := strings.Cut(field, "="); ok && fieldValue != "" {
			switch name {
			case "Credential":
				secrets = append(secrets, fieldValue)
				if key, _, ok := strings.Cut(fieldValue, "/"); ok && key != "" {
					secrets = append(secrets, key)
				}
			case "Signature":
				secrets = append(secrets, fieldValue)
			}
		}
	}
	if strings.HasPrefix(value, "AWS ") {
		if key, signature, ok := strings.Cut(strings.TrimPrefix(value, "AWS "), ":"); ok {
			secrets = append(secrets, key, signature)
		}
	}
	if scheme, payload, ok := strings.Cut(value, " "); ok && payload != "" {
		secrets = append(secrets, payload)
		if strings.EqualFold(scheme, "Basic") {
			if decoded, err := base64.StdEncoding.DecodeString(payload); err == nil {
				secrets = append(secrets, string(decoded))
				if _, password, ok := strings.Cut(string(decoded), ":"); ok {
					secrets = append(secrets, password)
				}
			}
		}
	}
	return secrets
}

// headerSecretValues collects the literal secrets carried by a header map.
func headerSecretValues(header http.Header) []string {
	var secrets []string
	for name, values := range header {
		if !isSecretHeaderName(name) && !isCustomHeaderName(name) {
			continue
		}
		for _, value := range values {
			secrets = append(secrets, authorizationSecretValues(value)...)
		}
	}
	return secrets
}

// redactSecretValues replaces literal secret strings in text this client did
// not construct.
func redactSecretValues(text string, secrets []string) string {
	return replaceSecretOccurrences(text, secrets)
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
	if req.Trailer != nil {
		redacted.Trailer = req.Trailer.Clone()
		redactHeader(redacted.Trailer)
	}

	if req.URL != nil {
		clonedURL := *req.URL
		redactURL(&clonedURL)
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
	secrets := headerSecretValues(req.Header)
	secrets = append(secrets, headerSecretValues(req.Trailer)...)
	if req.URL != nil {
		if req.URL.User != nil {
			secrets = append(secrets, req.URL.User.Username())
			if password, ok := req.URL.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
		if req.URL.RawQuery != "" {
			for name, values := range req.URL.Query() {
				if isSecretQueryParam(name) {
					secrets = append(secrets, values...)
				}
			}
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
	// Trailers are rendered after the body when it is dumped.
	if resp.Trailer != nil {
		redacted.Trailer = resp.Trailer.Clone()
		redactHeader(redacted.Trailer)
	}
	if withBody {
		redacted.Body = io.NopCloser(bytes.NewReader(body))
	}

	dump, err := httputil.DumpResponse(&redacted, withBody)
	if err != nil {
		return nil, err
	}

	// The body and any reflected header are server-controlled text: scrub
	// credential shapes generically, then the literal values this request
	// sent, then everything else the process knows to be secret.
	text := scrubCredentialText(string(dump))
	text = redactSecretValues(text, requestSecretValues(resp.Request))
	return []byte(scrubKnownSecrets(text)), nil
}
