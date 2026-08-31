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
	"strings"
	"testing"
)

func TestSecretRegistryScrubsRegisteredValues(t *testing.T) {
	resetSecretRegistryForTest()
	t.Cleanup(resetSecretRegistryForTest)

	registerSecret("longsecretvalue", "longsecretvalue-and-more", "", "short", redactedMarker)

	got := scrubKnownSecrets("a longsecretvalue-and-more b longsecretvalue c short d")
	if strings.Contains(got, "longsecretvalue") {
		t.Fatalf("registered secret survived: %s", got)
	}
	// Longest first: the longer secret is replaced whole, not left as a
	// marker with "-and-more" dangling after it.
	if strings.Contains(got, "-and-more") {
		t.Fatalf("longer secret was not replaced whole: %s", got)
	}
	// Values below the minimum length are never registered, so common
	// substrings are not mangled.
	if !strings.Contains(got, " short ") {
		t.Fatalf("short value must not be registered: %s", got)
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
