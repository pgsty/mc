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
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/minio/cli"
)

// The secret registry is the last line of defense for output this client
// prints but did not compose: an error message an endpoint reflected a header
// into, a trace annotation that carried an argument, a report line. Every
// credential the process learns - alias secret keys and session tokens, SSE-C
// keys, tier and service-principal secrets, passwords and tokens supplied on
// the command line, prompted for, or returned by an identity service - is
// registered as it is read, before it is validated or sent anywhere, and
// final error output is scrubbed against the registry before it reaches the
// terminal or a file.
//
// Per-request redaction in the tracer stays the primary control. The registry
// exists so that a path nobody thought to redact still cannot print a secret
// the process was handed.

// Secrets at or above secretRegistrySubstringAt are replaced wherever they
// occur. Shorter ones are replaced only as whole tokens, so a three-letter
// "secret" cannot turn every word containing it into a marker. Anything under
// secretRegistryMinLen is not treated as a secret at all.
const (
	secretRegistryMinLen      = 3
	secretRegistrySubstringAt = 8
)

var (
	secretRegistryMu sync.RWMutex
	secretRegistry   []string
)

// registerSecret records credential material so scrubKnownSecrets can remove
// it from any later output. Empty and very short values are ignored.
func registerSecret(values ...string) {
	secretRegistryMu.Lock()
	defer secretRegistryMu.Unlock()
	for _, value := range values {
		if len(value) < secretRegistryMinLen || value == redactedMarker {
			continue
		}
		duplicate := false
		for _, existing := range secretRegistry {
			if existing == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			secretRegistry = append(secretRegistry, value)
		}
	}
	sort.SliceStable(secretRegistry, func(i, j int) bool {
		return len(secretRegistry[i]) > len(secretRegistry[j])
	})
}

// registerAuthorizationSecrets registers an Authorization-style value in every
// form a server might echo it: whole, payload only, and for Basic the decoded
// credentials and the password on its own.
func registerAuthorizationSecrets(value string) {
	registerSecret(authorizationSecretValues(value)...)
}

// registerSecretFlags registers the values of the named flags, looking at the
// command level and the app level, so a flag given before the command name is
// covered too.
func registerSecretFlags(ctx *cli.Context, names ...string) {
	for _, name := range names {
		registerSecret(ctx.String(name), ctx.GlobalString(name))
	}
}

// registerTierSecretFlags registers every credential a remote tier command
// accepts, before validation or network activity. Shared by add and edit so
// the two cannot drift.
func registerTierSecretFlags(ctx *cli.Context) {
	registerSecretFlags(ctx, "secret-key", "account-key", "az-sp-client-secret", "api-key", "ldap-password")
}

// secretKeyValueFragments marks the keys in a key=value argument list whose
// values are credential material: admin config, LDAP and OpenID settings all
// carry bind passwords and client secrets this way.
var secretKeyValueFragments = []string{"secret", "password", "token", "passwd", "credential", "private_key", "api_key", "apikey"}

func isSecretKeyValueName(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range secretKeyValueFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// registerKeyValueSecrets registers the value of every key=value argument
// whose key names a secret, and returns the arguments with those values
// replaced, for use in an error message that would otherwise echo them.
func registerKeyValueSecrets(args []string) []string {
	redacted := make([]string, 0, len(args))
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if ok && isSecretKeyValueName(key) {
			registerSecret(strings.Trim(value, `"'`), value)
			redacted = append(redacted, key+"="+redactedMarker)
			continue
		}
		redacted = append(redacted, arg)
	}
	return redacted
}

// registerCredentialsJSON registers a credentials file such as a GCS service
// account key: the whole document, the private key on its own since that is
// the part a server would most plausibly echo, and the base64 form madmin
// sends over the admin API.
func registerCredentialsJSON(data []byte) {
	trimmed := strings.TrimSpace(string(data))
	registerSecret(trimmed, string(data))
	var fields map[string]any
	if json.Unmarshal(data, &fields) == nil {
		for name, value := range fields {
			if text, ok := value.(string); ok && isSecretKeyValueName(name) {
				registerSecret(text)
			}
		}
	}
	registerSecret(base64.StdEncoding.EncodeToString(data))
}

// occurrenceIntervals returns the [start,end) spans where secret occurs in
// text. Short secrets count only as whole tokens.
func occurrenceIntervals(text, secret string) [][2]int {
	var spans [][2]int
	whole := len(secret) >= secretRegistrySubstringAt
	from := 0
	for {
		i := strings.Index(text[from:], secret)
		if i < 0 {
			return spans
		}
		start := from + i
		end := start + len(secret)
		if whole || (isTokenBoundary(text, start-1) && isTokenBoundary(text, end)) {
			spans = append(spans, [2]int{start, end})
		}
		// Advance one byte, not len(secret): "abc abc" must yield both.
		from = start + 1
		if from > len(text) {
			return spans
		}
	}
}

func isTokenBoundary(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	c := text[i]
	isWord := c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return !isWord
}

// replaceSecretOccurrences replaces every occurrence of every secret in text
// with the marker. Occurrences are collected first and merged, so overlapping
// secrets and adjacent repeats are each replaced exactly once.
func replaceSecretOccurrences(text string, secrets []string) string {
	var spans [][2]int
	for _, secret := range secrets {
		if len(secret) < secretRegistryMinLen || secret == redactedMarker {
			continue
		}
		spans = append(spans, occurrenceIntervals(text, secret)...)
		// The JSON rendering of the secret, for text that is itself JSON.
		if encoded, err := json.Marshal(secret); err == nil {
			if escaped := string(encoded[1 : len(encoded)-1]); escaped != secret {
				spans = append(spans, occurrenceIntervals(text, escaped)...)
			}
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var out strings.Builder
	cursor := 0
	for i := 0; i < len(spans); {
		start, end := spans[i][0], spans[i][1]
		j := i + 1
		for j < len(spans) && spans[j][0] <= end {
			if spans[j][1] > end {
				end = spans[j][1]
			}
			j++
		}
		if start >= cursor {
			out.WriteString(text[cursor:start])
			out.WriteString(redactedMarker)
			cursor = end
		}
		i = j
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// scrubKnownSecrets replaces every registered secret in text.
func scrubKnownSecrets(text string) string {
	secretRegistryMu.RLock()
	secrets := append([]string(nil), secretRegistry...)
	secretRegistryMu.RUnlock()
	return replaceSecretOccurrences(text, secrets)
}

// scrubSecretsFromOutput prepares text for the terminal or a file: credential
// shapes are removed by pattern, then every registered secret by value.
func scrubSecretsFromOutput(text string) string {
	return scrubKnownSecrets(scrubCredentialText(text))
}

// scrubJSONValue walks a decoded JSON document and scrubs every string leaf.
// Object keys are left alone: they are schema, and a machine reading the
// document must still find them even if one happens to equal a secret.
func scrubJSONValue(v any) any {
	switch value := v.(type) {
	case string:
		return scrubSecretsFromOutput(value)
	case []any:
		for i := range value {
			value[i] = scrubJSONValue(value[i])
		}
		return value
	case map[string]any:
		for key := range value {
			value[key] = scrubJSONValue(value[key])
		}
		return value
	default:
		return v
	}
}

// scrubJSONOutput renders v as indented JSON with every string leaf scrubbed.
// The document is re-encoded from its decoded form rather than edited as text,
// so keys and structure survive intact. If the value cannot be round-tripped,
// the whole document is withheld rather than printed unscrubbed.
func scrubJSONOutput(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return "", err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(scrubJSONValue(decoded)); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// resetSecretRegistryForTest clears the registry between tests.
func resetSecretRegistryForTest() {
	secretRegistryMu.Lock()
	defer secretRegistryMu.Unlock()
	secretRegistry = nil
}
