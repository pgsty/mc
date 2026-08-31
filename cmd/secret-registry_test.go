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
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretRegistryScrubsRegisteredValues(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)

	registerSecret("longsecretvalue", "longsecretvalue-and-more", "", "short", "ab", redactedMarker)

	got := scrubKnownSecrets("a longsecretvalue-and-more b longsecretvalue c short d shortage ab")
	if strings.Contains(got, "longsecretvalue") {
		t.Fatalf("registered secret survived: %s", got)
	}
	// Longest first: the longer secret is replaced whole, not left as a
	// marker with "-and-more" dangling after it.
	if strings.Contains(got, "-and-more") {
		t.Fatalf("longer secret was not replaced whole: %s", got)
	}
	// A short value is scrubbed as a whole token only.
	if strings.Contains(got, " short ") || !strings.Contains(got, "shortage") {
		t.Fatalf("short value must be scrubbed as a token and nowhere else: %s", got)
	}
	// Two characters are never a secret.
	if !strings.HasSuffix(got, " ab") {
		t.Fatalf("two-character value must not be registered: %s", got)
	}
}

func TestScrubSecretsFromOutputCombinesShapesAndValues(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerSecret("MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE=")

	text := "Unable to stat: server said Credential=minioadmin/20260831/us-east-1/s3/aws4_request " +
		"key MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE= Signature=0123abcd"
	got := scrubSecretsFromOutput(text)
	for _, leaked := range []string{"minioadmin", "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE=", "0123abcd"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%q survived output scrubbing: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "Unable to stat: server said") {
		t.Fatalf("surrounding text must survive: %s", got)
	}
}

// Every credential the process reads must land in the registry, so the paths
// that read them are checked here rather than trusted.
func TestCredentialReadersRegisterSecrets(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)

	// MC_HOST_* style URL with a session token.
	if _, _, _, _, err := parseEnvURLStr("https://accesskey:envsecretkey123:envsessiontoken456@localhost:9000"); err != nil {
		t.Fatal(err)
	}
	// An SSE-C key spec, in the user's form and in the header's form.
	if _, _, _, err := parseSSEKey("mysilo/bucket=MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE", sseC); err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{
		"envsecretkey123", "envsessiontoken456",
		"MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE",
		"MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE=",
	} {
		if got := scrubKnownSecrets("[" + secret + "]"); got != "["+redactedMarker+"]" {
			t.Errorf("%q was not registered: %q", secret, got)
		}
	}
}

func TestRedactCredentialURLWithholdsAllUserinfo(t *testing.T) {
	for raw, want := range map[string]string{
		"https://key:secret@host:9000/bucket":      "https://" + redactedMarker + "@host:9000/bucket",
		"https://key:secret:token@host/bucket?x=1": "https://" + redactedMarker + "@host/bucket?x=1",
		"https://onlyuser@host":                    "https://" + redactedMarker + "@host",
		"key:secret@host/path":                     redactedMarker + "@host/path",
		"https://host/path":                        "https://host/path",
		// An "@" later in the URL is over-redacted on purpose: this text is
		// about to be printed, and a secret key may itself contain "/".
		"https://host/path?redirect=a@b":       "https://" + redactedMarker + "@b",
		"http://user:p a s s@ho st":            "http://" + redactedMarker + "@ho st",
		"https://user:pa/ss@host/x":            "https://" + redactedMarker + "@host/x",
		"https://user:p+a/s=s@host:9000/x?y=1": "https://" + redactedMarker + "@host:9000/x?y=1",
	} {
		if got := redactCredentialURL(raw); got != want {
			t.Errorf("redactCredentialURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Error paths that take a credential-bearing URL must not print it.
func TestEnvAliasErrorsDoNotEchoCredentials(t *testing.T) {
	const secret = "envsecretkey123"
	for name, raw := range map[string]string{
		"path in URL":        "https://key:" + secret + "@host/path",
		"query in URL":       "https://key:" + secret + "@host/?x=1",
		"unparseable host":   "https://key:" + secret + "@ho st",
		"unsupported scheme": "ftp://key:" + secret + "@host",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := expandAliasFromEnv(raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			rendered := err.ToGoError().Error() + " " + err.String() + " " + renderProbeTraceEnv(err)
			if strings.Contains(rendered, secret) {
				t.Fatalf("secret leaked: %s", rendered)
			}
		})
	}

	err := errInvalidURL("https://key:" + secret + "@host/path")
	if strings.Contains(err.ToGoError().Error(), secret) {
		t.Fatalf("errInvalidURL echoed the secret: %s", err.ToGoError().Error())
	}
}

// A malformed URL with the three-part key:secret:token userinfo still
// parses the way it always did.
func TestParseEnvURLStrStillAcceptsTokenForm(t *testing.T) {
	u, ak, sk, token, err := parseEnvURLStr("https://ak:sk123456:tok123456@localhost:9000")
	if err != nil {
		t.Fatal(err)
	}
	if ak != "ak" || sk != "sk123456" || token != "tok123456" || u.Host != "localhost:9000" {
		t.Fatalf("unexpected parse: %s %s %s %s", ak, sk, token, u.Host)
	}
}

// A secret shorter than eight characters is still scrubbed, but only as a
// whole token, so a common substring is never mangled.
func TestSecretRegistryShortSecretsMatchWholeTokens(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerSecret("abc", "ab")

	got := scrubKnownSecrets("token=abc, abcdef, (abc) abc\n")
	if strings.Contains(got, "token=abc,") || strings.Contains(got, "(abc)") || !strings.HasSuffix(got, redactedMarker+"\n") {
		t.Fatalf("short secret not scrubbed as a token: %q", got)
	}
	if !strings.Contains(got, "abcdef") {
		t.Fatalf("short secret must not be scrubbed inside a longer word: %q", got)
	}
	if got := scrubKnownSecrets("ab cab"); strings.Contains(got, redactedMarker) {
		t.Fatalf("two-character values must not be treated as secrets: %q", got)
	}
}

// --json error output escapes quotes, backslashes and HTML characters, so the
// registry must match the escaped rendering too.
func TestSecretRegistryScrubsJSONEscapedForms(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	const secret = "tok<en>&\"quoted\"\\back"
	registerSecret(secret)

	encoded, err := json.Marshal(map[string]string{"message": "server said " + secret})
	if err != nil {
		t.Fatal(err)
	}
	got := scrubKnownSecrets(string(encoded))
	if strings.Contains(got, "quoted") || strings.Contains(got, "\\u003cen") || strings.Contains(got, "tok") {
		t.Fatalf("JSON-escaped secret survived: %s", got)
	}
}

// Exact values must be removed before the broad credential-shape sweep. If
// the sweep sees a quote first it can otherwise consume only a prefix and
// leave the rest of a registered secret behind.
func TestScrubSecretsFromOutputRemovesKnownValueBeforeShapeSweep(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)

	const secret = `json"secret-token`
	registerSecret(secret)
	text := `reflected: auth=[AWS4-HMAC-SHA256 Credential=x/20260831/r/s/aws4_request, Signature=abc] token=[` + secret + `]`
	got := scrubSecretsFromOutput(text)
	if strings.Contains(got, "secret-token") || strings.Contains(got, secret) {
		t.Fatalf("known secret was split by the shape sweep: %s", got)
	}
}

func TestAuthorizationSecretValuesCoverEchoableParts(t *testing.T) {
	values := authorizationSecretValues("Basic " + base64.StdEncoding.EncodeToString([]byte("user:hunter2pass")))
	joined := strings.Join(values, "|")
	for _, want := range []string{"user:hunter2pass", "hunter2pass", base64.StdEncoding.EncodeToString([]byte("user:hunter2pass"))} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q missing from %v", want, values)
		}
	}
	values = authorizationSecretValues("AWS4-HMAC-SHA256 Credential=minioadmin/20260831/us-east-1/s3/aws4_request, Signature=deadbeef")
	joined = strings.Join(values, "|")
	for _, want := range []string{"|minioadmin|", "|deadbeef"} {
		if !strings.Contains(joined+"|", want) {
			t.Errorf("%q missing from %v", want, values)
		}
	}
}

func TestRegisterCredentialsJSON(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerCredentialsJSON([]byte(`{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\nabcdef\n-----END PRIVATE KEY-----\n","client_email":"x@y"}`))
	if got := scrubKnownSecrets("key: -----BEGIN PRIVATE KEY-----\nabcdef\n-----END PRIVATE KEY-----\n end"); strings.Contains(got, "abcdef") {
		t.Fatalf("private key not registered: %q", got)
	}
}

func TestInvalidAPISignatureErrorDoesNotEchoCredentials(t *testing.T) {
	msg := errInvalidAPISignature("bad", "https://key:envsecretkey123@host").ToGoError().Error()
	if strings.Contains(msg, "envsecretkey123") {
		t.Fatalf("errInvalidAPISignature echoed the secret: %s", msg)
	}
}

// Adjacent repeats share one delimiter; both must go.
func TestSecretRegistryScrubsAdjacentRepeats(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerSecret("abc")
	if got := scrubKnownSecrets("abc abc"); got != redactedMarker+" "+redactedMarker {
		t.Fatalf("adjacent repeats: %q", got)
	}
	if got := scrubKnownSecrets("abc,abc;abc"); got != redactedMarker+","+redactedMarker+";"+redactedMarker {
		t.Fatalf("adjacent repeats with punctuation: %q", got)
	}
	registerSecret("longsecretvalue1")
	if got := scrubKnownSecrets("longsecretvalue1longsecretvalue1 x"); got != redactedMarker+" x" {
		t.Fatalf("overlapping long secrets must merge into one marker: %q", got)
	}
}

// Scrubbing JSON must never rewrite a key: a machine reading the document
// still needs its schema even when a secret happens to equal a key name.
func TestScrubJSONOutputKeepsKeys(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	registerSecret("status", "tok<en>&0123456789")

	got, err := scrubJSONOutput(map[string]any{
		"status": "error",
		"error": map[string]any{
			"message": "server said tok<en>&0123456789 and status",
			"list":    []any{"status", 1, true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"status": "error"`) {
		t.Fatalf("key rewritten: %s", got)
	}
	if strings.Contains(got, "tok") || strings.Contains(got, "u003cen") {
		t.Fatalf("value not scrubbed: %s", got)
	}
	if !strings.Contains(got, `"`+redactedMarker+`",`) {
		t.Fatalf("string leaf inside a list not scrubbed: %s", got)
	}
	if strings.Contains(got, "and status\"") {
		t.Fatalf("whole-token short secret in a value not scrubbed: %s", got)
	}
}

func TestRegisterKeyValueSecrets(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)
	redacted := registerKeyValueSecrets([]string{"server_addr=ldap:389", "lookup_bind_password=bindpass0123", `client_secret="quoted0123"`, "enable=on"})
	want := []string{"server_addr=ldap:389", "lookup_bind_password=" + redactedMarker, "client_secret=" + redactedMarker, "enable=on"}
	if strings.Join(redacted, "|") != strings.Join(want, "|") {
		t.Fatalf("redacted args = %v, want %v", redacted, want)
	}
	for _, secret := range []string{"bindpass0123", "quoted0123"} {
		if got := scrubKnownSecrets("[" + secret + "]"); got != "["+redactedMarker+"]" {
			t.Errorf("%q was not registered: %q", secret, got)
		}
	}
}
