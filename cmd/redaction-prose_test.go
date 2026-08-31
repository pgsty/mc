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

// The credential shapes are swept over every error message, so a shape that
// also matches prose would mangle messages this client composes itself.
func TestScrubCredentialTextKeepsProse(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	for _, text := range []string{
		"Invalid configuration for AWS S3 compatible remote tier",
		"cannot be combined with AWS role authentication",
		"access token not found in response",
		"The provided token has expired",
		"The security token included in the request is invalid",
		"Content-MD5 digest mismatch for part 3",
		"Basic auth is disabled",
		"AWS4-HMAC-SHA256 is required for this region",
		"Negotiate authentication is not supported",
		"Token expired",
		"TOKEN EXPIRED",
		"NTLM is deprecated",
		"Invalid endpoint for AWS https://s3.amazonaws.com",
		"AWS us-east-1:GetObject denied",
		"Content digest SHA256:0123abcd mismatch for part 3",
		"?list-type=2&max-keys=1000&prefix=foo&continuation-token=abc0123&key-marker=obj",
	} {
		if got := scrubSecretsFromOutput(text); got != text {
			t.Errorf("scrubSecretsFromOutput(%q) = %q, want it unchanged", text, got)
		}
	}
}

// Tightening the shapes must not let an echoed credential through.
func TestScrubCredentialTextStillRedactsCredentialShapes(t *testing.T) {
	for text, want := range map[string]string{
		"got Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature back": "got Bearer " + redactedMarker + " back",
		"Basic dXNlcjpwYXNz":                                    "Basic " + redactedMarker,
		"Proxy-Authorization: Basic dXNlcjpodW50ZXIy":           "Proxy-Authorization: Basic " + redactedMarker,
		"AWS AKIAIOSFODNN7EXAMPLE:frJIUN8DYpKDtOLCwo//yllqDzg=": "AWS " + redactedMarker,
		"AWS a/b:frJIUN8DYpKDtOLCwo//yllqDzg=":                  "AWS " + redactedMarker,
		"Token 0123456789abcdef":                                "Token " + redactedMarker,
		"Negotiate YIIFmQYGKwYBBQUCoIIFjTCCBYmgJDAi":            "Negotiate " + redactedMarker,
		"AWS4-HMAC-SHA256 Credential=k/20260831/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=ab12": "AWS4-HMAC-SHA256 " + redactedMarker,
		"AWS4-HMAC-SHA256 SECRETVALUE0123 Credential=k/20260831/us-east-1/s3/aws4_request":                     "AWS4-HMAC-SHA256 " + redactedMarker,
		"scope Credential=k/20260831/us-east-1/s3/aws4_request only":                                           "scope Credential=" + redactedMarker + " only",
		"http://h/x?X-Amz-Security-Token=PROBE0123&y=1":                                                        "http://h/x?X-Amz-Security-Token=" + redactedMarker + "&y=1",
	} {
		if got := scrubCredentialText(text); got != want {
			t.Errorf("scrubCredentialText(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestLooksLikeCredentialPayload(t *testing.T) {
	for payload, want := range map[string]bool{
		"auth":                  false,
		"authentication":        false,
		"Authentication":        false,
		"expired":               false,
		"mismatch":              false,
		"dXNlcjpwYXNz":          true,
		"0123456789abcdef":      true,
		"eyJhbGciOiJIUzI1NiJ9.": true,
		"some-long-secret":      true,
		"ABCDEFGHIJKLMNOP":      true,
	} {
		if got := looksLikeCredentialPayload(payload); got != want {
			t.Errorf("looksLikeCredentialPayload(%q) = %v, want %v", payload, got, want)
		}
	}
}

// A trace's raw query has no leading "?", so a credential in the first
// parameter needs the anchor put back before the query shape can see it.
func TestRedactTraceQueryRedactsFirstParameter(t *testing.T) {
	for raw, want := range map[string]string{
		"": "",
		"X-Amz-Security-Token=PROBESESSIONTOKEN0123&X-Amz-Signature=SIG0123&versionId=keep": "X-Amz-Security-Token=" + redactedMarker + "&X-Amz-Signature=" + redactedMarker + "&versionId=keep",
		"token=abc0123&x=1":          "token=" + redactedMarker + "&x=1",
		"AWSAccessKeyId=AKIA0123":    "AWSAccessKeyId=" + redactedMarker,
		"versionId=keep&list-type=2": "versionId=keep&list-type=2",
		"max-keys=1000&key-marker=obj&continuation-token=abc0123&x-amz-security-token=tok0123": "max-keys=1000&key-marker=obj&continuation-token=abc0123&x-amz-security-token=" + redactedMarker,
	} {
		if got := redactTraceQuery(raw, nil); got != want {
			t.Errorf("redactTraceQuery(%q) = %q, want %q", raw, got, want)
		}
	}
}

func shortTraceProbeEvent(sentinel string) madmin.ServiceTraceInfo {
	return madmin.ServiceTraceInfo{
		Trace: madmin.TraceInfo{
			TraceType: madmin.TraceS3,
			NodeName:  "node1",
			FuncName:  "s3.GetObject",
			Time:      time.Unix(100, 0).UTC(),
			Path:      "/bucket/object",
			Error:     "denied for Bearer " + sentinel,
			Message:   "token=" + sentinel,
			Custom:    map[string]string{"note": "Authorization: Bearer " + sentinel},
			HTTP: &madmin.TraceHTTPStats{
				ReqInfo: madmin.TraceRequestInfo{
					Time:     time.Unix(100, 0).UTC(),
					Proto:    "HTTP/1.1",
					Method:   http.MethodGet,
					Path:     "/bucket/object",
					RawQuery: "X-Amz-Security-Token=" + sentinel + "&X-Amz-Signature=" + sentinel + "&versionId=keep-version",
					Headers:  map[string][]string{"X-Amz-Security-Token": {sentinel}, "User-Agent": {"keep-user-agent"}},
					Client:   "10.0.0.1",
				},
				RespInfo: madmin.TraceResponseInfo{
					Time:       time.Unix(101, 0).UTC(),
					StatusCode: http.StatusForbidden,
				},
			},
		},
	}
}

// The default (non-verbose) `admin trace` rendering prints the request's
// query string, error and annotations; it must withhold credentials exactly
// like the verbose rendering, and must not modify the shared event.
func TestShortTraceRedactsQueryErrorAndCustom(t *testing.T) {
	const sentinel = "SHORTTRACESENTINEL0123456789"
	event := shortTraceProbeEvent(sentinel)
	short := shortTrace(event)
	for name, out := range map[string]string{"text": short.String(), "json": short.JSON()} {
		if strings.Contains(out, sentinel) {
			t.Errorf("short trace %s output leaks the sentinel:\n%s", name, out)
		}
		if !strings.Contains(out, "versionId=keep-version") {
			t.Errorf("short trace %s output lost the harmless query parameter:\n%s", name, out)
		}
	}
	if short.Query != "X-Amz-Security-Token="+redactedMarker+"&X-Amz-Signature="+redactedMarker+"&versionId=keep-version" {
		t.Errorf("short trace query = %q", short.Query)
	}
	if !strings.Contains(event.Trace.HTTP.ReqInfo.RawQuery, sentinel) || !strings.Contains(event.Trace.Custom["note"], sentinel) || !strings.Contains(event.Trace.Error, sentinel) {
		t.Errorf("the shared event was modified: %+v", event.Trace)
	}
}

// The verbose rendering must catch a credential in the first query parameter
// and in the event's error, message and custom annotations.
func TestVerboseTraceRedactsLeadingQueryTokenAndAnnotations(t *testing.T) {
	const sentinel = "VERBOSETRACESENTINEL0123456789"
	msg := traceMessage{Status: "success", ServiceTraceInfo: shortTraceProbeEvent(sentinel)}
	for name, out := range map[string]string{"text": msg.String(), "json": msg.JSON()} {
		if strings.Contains(out, sentinel) {
			t.Errorf("verbose trace %s output leaks the sentinel:\n%s", name, out)
		}
		if !strings.Contains(out, "versionId=keep-version") || !strings.Contains(out, "keep-user-agent") {
			t.Errorf("verbose trace %s output lost harmless content:\n%s", name, out)
		}
	}
	if !strings.Contains(msg.Trace.HTTP.ReqInfo.RawQuery, sentinel) || !strings.Contains(msg.Trace.Custom["note"], sentinel) {
		t.Errorf("the shared event was modified: %+v", msg.Trace)
	}
}

// "auth_token=off" and "client_secret=true" carry no secret; registering
// them would redact every later "off" and "true".
func TestRegisterSecretIgnoresPlaceholders(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerKeyValueSecrets([]string{"auth_token=off", "client_secret=true", `token="none"`, "password=Enabled"})
	registerSecret("default", "OFF", "unset")
	const text = "turn it off; is it true? none by default; OFF and unset stay; Enabled too"
	if got := scrubKnownSecrets(text); got != text {
		t.Errorf("placeholders were registered as secrets: %q", got)
	}
}

// admin config set values embed credentials the key name does not announce.
func TestRegisterKeyValueSecretsCoversEmbeddedCredentials(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	redacted := registerKeyValueSecrets([]string{
		"notify_postgres:1",
		"connection_string=host=db user=u password=pgSecret0123 dbname=x",
		"notify_amqp:1",
		"url=amqp://user:amqpSecret0123@host:5672",
		"endpoint=https://h/hook?token=hookToken0123",
		"dsn_string=user:mysqlSecret0123@tcp(db:3306)/db",
		"queue_dir=/var/queue",
		"broker=amqps://svc:p%40ss%3Aw0rd@broker.example:5671/vhost",
		"connection_string=host=db password='sp ace0123' sslmode=disable",
		"connection_string=host=db user=u password=password sslmode=disable",
	})
	joined := strings.Join(redacted, " ")
	for _, secret := range []string{"pgSecret0123", "amqpSecret0123", "hookToken0123", "p%40ss%3Aw0rd", "p@ss:w0rd", "sp ace0123"} {
		if strings.Contains(joined, secret) {
			t.Errorf("redacted arguments still carry %q: %s", secret, joined)
		}
		if got := scrubKnownSecrets("[" + secret + "]"); got != "["+redactedMarker+"]" {
			t.Errorf("%q was not registered: %q", secret, got)
		}
	}
	for _, keep := range []string{"notify_postgres:1", "host=db user=u password=" + redactedMarker + " dbname=x", "amqp://user:" + redactedMarker + "@host:5672", "https://h/hook?token=" + redactedMarker, "queue_dir=/var/queue", "amqps://svc:" + redactedMarker + "@broker.example:5671/vhost", "password='" + redactedMarker + "'", "password=password sslmode=disable"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("redacted arguments lost %q: %s", keep, joined)
		}
	}
	// A DSN whose password sits in a form neither rule recognizes is at least
	// still reported as given, without breaking anything else.
	if !strings.Contains(joined, "dsn_string=user:mysqlSecret0123@tcp(db:3306)/db") {
		t.Errorf("unrecognized DSN form was altered: %s", joined)
	}
	// The documentation's own placeholder must not poison later output.
	if got := scrubKnownSecrets("invalid password for user u"); got != "invalid password for user u" {
		t.Errorf("the literal word password was registered: %q", got)
	}
}

// A doubled space after the scheme must not register the payload with a
// leading space it is never echoed with.
func TestAuthorizationSecretValuesTrimPayloadSpaces(t *testing.T) {
	found := false
	for _, value := range authorizationSecretValues("Bearer  probeTokenValue0123") {
		if value == "probeTokenValue0123" {
			found = true
		}
	}
	if !found {
		t.Errorf("payload after a doubled space was not registered: %v", authorizationSecretValues("Bearer  probeTokenValue0123"))
	}
}
