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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Black-box check of the whole output path. A hostile endpoint reflects every
// credential the client sent into a 400 response - header, cookie, redirect
// and body - and the client is run as a real subprocess with --debug, so the
// trace, the command's own result lines and the final error all go through
// the same output the user would see.
const (
	blackboxSecretKey    = "blackboxsecretkey0123456789"
	blackboxSessionToken = "blackboxsessiontoken0123456789"
	blackboxTokenHeader  = "blackboxauthtokenvalue0123456789"
	blackboxSSECKey      = "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE"
	blackboxCookie       = "blackboxcookievalue0123456789"
)

func newReflectingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Has("location") {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		if r.Method == http.MethodHead {
			// Let checksum verify get past its HEAD so the GET below, which
			// carries the SSE-C key, is what fails and gets reflected.
			w.Header().Set("Content-Length", "4")
			w.Header().Set("Last-Modified", "Mon, 31 Aug 2026 00:00:00 GMT")
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("X-Amz-Checksum-Type", checksumVerifyFullObjectType)
			w.Header().Set("X-Amz-Checksum-Crc32", "AAAAAA==")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("Set-Cookie", "sid="+blackboxCookie)
		w.Header().Set("Location", "https://user:"+blackboxSecretKey+"@evil.example/x?X-Amz-Signature=deadbeef")
		w.Header().Set("Authorization", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<Error><Code>InvalidRequest</Code><Message>reflected: auth=[%s] token=[%s] ssec=[%s]</Message><Resource>%s</Resource><RequestId>1</RequestId></Error>`,
			r.Header.Get("Authorization"),
			r.Header.Get("X-Auth-Token"),
			r.Header.Get("X-Amz-Server-Side-Encryption-Customer-Key"),
			r.URL.Path)
	}))
}

func assertNoSecretInOutput(t *testing.T, label string, result checksumVerifyCLIResult) {
	t.Helper()
	combined := string(result.stdout) + "\n" + string(result.stderr)
	if strings.TrimSpace(combined) == "" {
		t.Fatalf("%s: produced no output at all", label)
	}
	for name, secret := range map[string]string{
		"secret key":         blackboxSecretKey,
		"session token":      blackboxSessionToken,
		"custom auth token":  blackboxTokenHeader,
		"SSE-C key (raw)":    blackboxSSECKey,
		"SSE-C key (padded)": blackboxSSECKey + "=",
		"cookie":             blackboxCookie,
		"signature":          "Signature=deadbeef",
	} {
		if strings.Contains(combined, secret) {
			t.Errorf("%s: %s leaked into output:\n%s", label, name, combined)
		}
	}
	// The SigV4 credential the client signed with must never appear with
	// its access key attached, in the request trace or in the reflected body.
	if strings.Contains(combined, "Credential=blackboxaccess/") {
		t.Errorf("%s: access key leaked inside a Credential scope:\n%s", label, combined)
	}
	if !strings.Contains(combined, redactedMarker) {
		t.Errorf("%s: expected redaction markers in the output:\n%s", label, combined)
	}
}

func TestBlackboxNoCredentialReachesOutput(t *testing.T) {
	server := newReflectingServer(t)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	env := []string{"MC_HOST_b4=http://blackboxaccess:" + blackboxSecretKey + ":" + blackboxSessionToken + "@" + host}

	// A plain command that ends in fatalIf: the reflected message is the
	// final error line.
	assertNoSecretInOutput(t, "stat", runChecksumVerifyCLI(t, server.URL, env,
		"--debug", "--custom-header", "X-Auth-Token: "+blackboxTokenHeader, "stat", "b4/archive/object"))

	// The same in JSON, which renders the error through a different path.
	assertNoSecretInOutput(t, "stat --json", runChecksumVerifyCLI(t, server.URL, env,
		"--debug", "--json", "--custom-header", "X-Auth-Token: "+blackboxTokenHeader, "stat", "b4/archive/object"))

	// checksum verify records the server message in its own result line and
	// sends an SSE-C key the server reflects too. Without --debug, so the
	// result line is the only place the reflected text can reach.
	result := runChecksumVerifyCLI(t, server.URL, env,
		"--custom-header", "X-Auth-Token: "+blackboxTokenHeader,
		"checksum", "verify", "--enc-c", "b4/archive="+blackboxSSECKey, "--fail-on", "none", "b4/archive/object")
	combined := string(result.stdout) + "\n" + string(result.stderr)
	if !strings.Contains(combined, "reflected: auth=[") {
		t.Fatalf("checksum verify did not surface the reflected server message, so this proves nothing:\n%s", combined)
	}
	assertNoSecretInOutput(t, "checksum verify", result)
}

// The non-debug error paths that take user input must not print it back.
func TestBlackboxUserInputErrorsDoNotEchoSecrets(t *testing.T) {
	server := newReflectingServer(t)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")

	for name, tc := range map[string]struct {
		env  []string
		args []string
		// want, when set, must appear in the output: it pins the failure to
		// the validation under test rather than to the mock's 400.
		want string
	}{
		"MC_HOST with a path": {
			env:  []string{"MC_HOST_bad=http://blackboxaccess:" + blackboxSecretKey + "@" + host + "/path"},
			args: []string{"ls", "bad/"},
		},
		"MC_HOST that does not parse": {
			env:  []string{"MC_HOST_bad=http://blackboxaccess:" + blackboxSecretKey + "@ho st"},
			args: []string{"ls", "bad/"},
		},
		"alias set with credentials in the URL": {
			args: []string{"alias", "set", "x", "http://blackboxaccess:" + blackboxSecretKey + "@" + host + "/bucket", "blackboxaccess", blackboxSecretKey},
		},
		"alias set with a short secret": {
			args: []string{"alias", "set", "x", "http://" + host, "blackboxaccess", "short"},
		},
		"alias set with too many arguments": {
			args: []string{"alias", "set", "x", "http://" + host, "blackboxaccess", blackboxSecretKey, "extra"},
		},
		"custom header without a colon, app level": {
			args: []string{"--custom-header", "Authorization Bearer " + blackboxTokenHeader, "ls", "b4/"},
			want: "invalid custom header entry #1: expected name:value",
		},
		"custom header with an invalid value, app level": {
			args: []string{"--custom-header", "Authorization: Bearer " + blackboxTokenHeader + "\x01", "ls", "b4/"},
			want: "invalid custom header entry #1 (Authorization)",
		},
		"custom header without a colon, command level": {
			args: []string{"ls", "--custom-header", "Authorization Bearer " + blackboxTokenHeader, "b4/"},
			want: "invalid custom header entry #1: expected name:value",
		},
		"replication target with credentials": {
			env:  []string{"MC_HOST_b4=http://blackboxaccess:" + blackboxSecretKey + "@" + host},
			args: []string{"--debug", "replicate", "add", "b4/archive", "--remote-bucket", "http://blackboxaccess:" + blackboxSecretKey + "@" + host + "/bad bucket name"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := runChecksumVerifyCLI(t, server.URL, tc.env, tc.args...)
			combined := string(result.stdout) + "\n" + string(result.stderr)
			if result.exitCode == 0 {
				t.Fatalf("expected a failure, got exit 0:\n%s", combined)
			}
			if tc.want != "" && !strings.Contains(combined, tc.want) {
				t.Fatalf("expected %q in the output:\n%s", tc.want, combined)
			}
			for name, secret := range map[string]string{
				"secret key":   blackboxSecretKey,
				"custom token": blackboxTokenHeader,
			} {
				if strings.Contains(combined, secret) {
					t.Errorf("%s leaked into output:\n%s", name, combined)
				}
			}
		})
	}
}
