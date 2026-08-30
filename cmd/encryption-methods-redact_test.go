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

	"github.com/minio/mc/pkg/probe"
)

// renderProbeTraceEnv flattens the annotations Trace() records, which is what
// --debug prints alongside the message.
func renderProbeTraceEnv(err *probe.Error) string {
	var out strings.Builder
	for _, point := range err.CallTrace {
		for key, values := range point.Env {
			out.WriteString(key)
			out.WriteString("=")
			out.WriteString(strings.Join(values, ","))
			out.WriteString(" ")
		}
	}
	return out.String()
}

// A malformed SSE-C key is still key material: the user is one typo away from
// having pasted the real one, and the error reaches the terminal and CI logs
// whether or not --debug is set.
func TestParseSSEKeyDoesNotEchoClientKeyMaterial(t *testing.T) {
	for name, spec := range map[string]string{
		"undecodable":  "mysilo/bucket=not!valid!base64!or!hex!key!material!!",
		"wrong length": "mysilo/bucket=c2hvcnRrZXk",
	} {
		t.Run(name, func(t *testing.T) {
			material := spec[strings.LastIndex(spec, "=")+1:]
			_, _, _, err := parseSSEKey(spec, sseC)
			if err == nil {
				t.Fatalf("expected %s key to be rejected", name)
			}
			rendered := err.String() + " " + err.ToGoError().Error() + " " + renderProbeTraceEnv(err)
			if strings.Contains(rendered, material) {
				t.Fatalf("SSE-C key material leaked into the error: %s", rendered)
			}
			if !strings.Contains(rendered, "mysilo/bucket") {
				t.Fatalf("alias and prefix should survive so the error stays actionable: %s", rendered)
			}
		})
	}
}

// A KMS key name identifies a key the server holds; it is not a secret, and
// hiding it would leave the user with no way to spot their typo.
func TestParseSSEKeyKeepsKMSKeyName(t *testing.T) {
	_, _, _, err := parseSSEKey("mysilo/bucket=bad key name", sseKMS)
	if err == nil {
		t.Fatal("expected a malformed KMS key name to be rejected")
	}
	if !strings.Contains(err.String(), "bad key name") {
		t.Fatalf("KMS key name should stay in the message: %s", err.String())
	}
}

func TestRedactSSEKeySpec(t *testing.T) {
	const material = "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE"
	for name, spec := range map[string]string{
		"plain":              "mysilo/bucket=" + material,
		"padded key":         "mysilo/bucket=" + material + "=",
		"prefix contains eq": "mysilo/bucket/a=b=" + material,
	} {
		t.Run(name, func(t *testing.T) {
			got := redactSSEKeySpec(spec, sseC)
			if strings.Contains(got, material) {
				t.Fatalf("key material survived redaction: %q", got)
			}
			if !strings.HasSuffix(got, redactedMarker) {
				t.Fatalf("redaction marker missing: %q", got)
			}
			if !strings.HasPrefix(got, "mysilo/bucket") {
				t.Fatalf("alias and prefix should survive: %q", got)
			}
		})
	}

	const spec = "mysilo/bucket=" + material
	if got := redactSSEKeySpec(spec, sseKMS); got != spec {
		t.Fatalf("KMS spec should be untouched: %q", got)
	}
	// With no "=" there is no way to tell a prefix from a key, and the common
	// mistake is passing the bare key: withhold the whole string.
	if got := redactSSEKeySpec(material, sseC); got != redactedMarker {
		t.Fatalf("a spec with no separator must be withheld entirely: %q", got)
	}
	if got := redactSSEKeySpec("mysilo/bucket", sseC); got != redactedMarker {
		t.Fatalf("a spec with no separator must be withheld entirely: %q", got)
	}
	// "mysilo/bucket=" and a bare padded key "MzJi...=" have the same shape, so
	// neither can keep its leading segment.
	if got := redactSSEKeySpec("mysilo/bucket=", sseC); got != redactedMarker {
		t.Fatalf("a spec with nothing after the separator must be withheld: %q", got)
	}
	if got := redactSSEKeySpec(material+"=", sseC); got != redactedMarker {
		t.Fatalf("a bare padded key must be withheld: %q", got)
	}
}

// The likely mistake is `--enc-c "$KEY"` instead of `--enc-c alias/prefix=$KEY`.
// The parser rejects it either way; it must not print the key while doing so.
func TestParseSSEKeyDoesNotEchoAKeyPassedWithoutAPrefix(t *testing.T) {
	for name, spec := range map[string]string{
		"bare key":        "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE",
		"bare padded key": "MzJieXRlc2xvbmdzZWNyZXRrZXltdXN0YmVnaXZlbjE=",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := parseSSEKey(spec, sseC)
			if err == nil {
				t.Fatal("expected a prefix-less SSE-C spec to be rejected")
			}
			rendered := err.String() + " " + err.ToGoError().Error() + " " + renderProbeTraceEnv(err)
			if strings.Contains(rendered, strings.TrimSuffix(spec, "=")) {
				t.Fatalf("key material leaked into the error: %s", rendered)
			}
		})
	}
}
