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

// Credential redaction for --debug traces.
//
// Everything here is written fail-closed. A value is rebuilt from the parts
// that are known to be safe; whatever is not recognized is withheld, never
// passed through on the assumption that it holds nothing sensitive. Losing a
// field from a debug line costs a little diagnostic value; a leaked key costs
// a credential.

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

// Signature v4 credential: "Credential=<access-key-id>/<yyyymmdd>/<region>/<service>/aws4_request".
//
// The key is NOT delimited by the first "/": minio-go builds this by
// concatenating the raw access key with the scope, and this client only checks
// a key's length, so a key may hold slashes, spaces, commas, or even a
// "/YYYYMMDD/" fragment of its own. The leading group is greedy so the scope
// captured is the LAST one on the line; whatever precedes it is the key. Region
// and service are held to the character set real ones use.
var traceCredentialRegexp = regexp.MustCompile(`Credential=(.*)/(\d{8})/([a-z0-9-]+)/([a-z0-9-]+)/aws4_request`)

// SignedHeaders lists lowercase header names separated by ";".
var traceSignedHeadersRegexp = regexp.MustCompile(`^[a-z0-9!#$%&'*+.^_` + "`" + `|~-]+(?:;[a-z0-9!#$%&'*+.^_` + "`" + `|~-]+)*$`)

// Signature=<anything up to a delimiter>, in headers, query strings and echoed
// text. Not restricted to hex: the value is withheld whatever it holds.
var traceSignatureTextRegexp = regexp.MustCompile(`Signature=[^,\s"'<>&]+`)

// Signature v2 in free text: "AWS <access-key-id>:<signature>".
var traceV2TextRegexp = regexp.MustCompile(`\bAWS [^\s:]+:[^\s"'<>]+`)

// Credential-bearing query parameters wherever a URL appears in text, matched
// by name fragment so a parameter this client never sends is still caught.
var traceQuerySecretTextRegexp = regexp.MustCompile(`([?&][^=&\s"'<>]*(?i:token|signature|credential|secret|password|api[-_]?key|auth|sig)[^=&\s"'<>]*=)[^&\s"'<>]+`)

// Leading scheme token of an Authorization value: "Basic", "Bearer", ...
var traceAuthSchemeRegexp = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+`)

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

// isSecretQueryParam reports whether a query parameter carries credential
// material, for presigned URLs of either signature version and for anything a
// proxy in front of the endpoint might expect.
func isSecretQueryParam(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range []string{"token", "signature", "credential", "secret", "password", "api-key", "apikey", "api_key", "auth", "sig", "awsaccesskeyid"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// redactSigV4 rebuilds a Signature v4 Authorization value from the fields it
// recognizes: the credential scope, the SignedHeaders list, and the presence
// of a Signature. Every other field - and any field whose value does not have
// the expected shape - is withheld, so a secret cannot ride along in a field
// this client never sends or in a Signature that is not hex.
func redactSigV4(value string) string {
	algorithm, rest, _ := strings.Cut(value, " ")
	loc := traceCredentialRegexp.FindStringSubmatchIndex(rest)
	if loc == nil {
		return algorithm + " " + redactedMarker
	}
	scope := rest[loc[4]:loc[5]] + "/" + rest[loc[6]:loc[7]] + "/" + rest[loc[8]:loc[9]] + "/aws4_request"
	parts := []string{"Credential=" + redactedMarker + "/" + scope}
	if strings.TrimSpace(rest[:loc[0]]) != "" {
		parts = append([]string{redactedMarker}, parts...)
	}
	for _, field := range strings.Split(rest[loc[1]:], ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, fieldValue, _ := strings.Cut(field, "=")
		switch name {
		case "SignedHeaders":
			if traceSignedHeadersRegexp.MatchString(fieldValue) {
				parts = append(parts, "SignedHeaders="+fieldValue)
			} else {
				parts = append(parts, "SignedHeaders="+redactedMarker)
			}
		case "Signature":
			parts = append(parts, "Signature="+redactedMarker)
		default:
			// The name itself is not printed: it could be the secret.
			parts = append(parts, redactedMarker)
		}
	}
	return algorithm + " " + strings.Join(parts, ", ")
}

// redactAuthorization removes credential material from an Authorization or
// Proxy-Authorization value.
func redactAuthorization(value string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return ""
	case strings.HasPrefix(value, "AWS "):
		// Signature v2: "AWS <access-key-id>:<signature>". Nothing is safe.
		return "AWS " + redactedMarker + ":" + redactedMarker
	case strings.HasPrefix(value, "AWS4-"):
		return redactSigV4(value)
	default:
		// Basic, Bearer, or anything a caller attached with --custom-header.
		if scheme := traceAuthSchemeRegexp.FindString(value); scheme != "" && scheme != value {
			return scheme + " " + redactedMarker
		}
		return redactedMarker
	}
}

// redactHeader removes credential material from a cloned header map in
// place. Every value of a header is handled, never only the first.
func redactHeader(header http.Header) {
	for name, values := range header {
		if !isSecretHeaderName(name) {
			continue
		}
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "Authorization" || canonical == "Proxy-Authorization" {
			redacted := make([]string, 0, len(values))
			for _, value := range values {
				redacted = append(redacted, redactAuthorization(value))
			}
			header[name] = redacted
			continue
		}
		header[name] = []string{redactedMarker}
	}
	// A redirect target can carry a presigned signature or userinfo.
	if len(header.Values("Location")) > 0 {
		header.Set("Location", redactURLString(header.Get("Location")))
	}
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
	text = traceCredentialRegexp.ReplaceAllString(text, "Credential="+redactedMarker+"/$2/$3/$4/aws4_request")
	text = traceSignatureTextRegexp.ReplaceAllString(text, "Signature="+redactedMarker)
	text = traceV2TextRegexp.ReplaceAllString(text, "AWS "+redactedMarker+":"+redactedMarker)
	return traceQuerySecretTextRegexp.ReplaceAllString(text, "${1}"+redactedMarker)
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

// authorizationSecretValues returns the pieces of an Authorization-style value
// a server might echo on their own: the whole value, the payload after the
// scheme, a Basic payload decoded, a SigV4 access key and signature, a SigV2
// key and signature.
func authorizationSecretValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	secrets := []string{value}
	if match := traceCredentialRegexp.FindStringSubmatch(value); match != nil {
		secrets = append(secrets, match[1])
	}
	for _, field := range strings.Split(value, ",") {
		if name, fieldValue, ok := strings.Cut(strings.TrimSpace(field), "="); ok && name == "Signature" {
			secrets = append(secrets, fieldValue)
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
		if !isSecretHeaderName(name) {
			continue
		}
		for _, value := range values {
			secrets = append(secrets, authorizationSecretValues(value)...)
		}
	}
	return secrets
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
