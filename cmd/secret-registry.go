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
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The secret registry is the last line of defense for output this client
// prints but did not compose: an error message an endpoint reflected a header
// into, a trace annotation that carried an argument, a report line. Every
// credential the process learns - alias secret keys and session tokens, SSE-C
// keys, tier and service-principal secrets, passwords and tokens supplied on
// the command line - is registered as it is read, and final error output is
// scrubbed against the registry before it reaches the terminal or a file.
//
// Per-request redaction in the tracer stays the primary control. The registry
// exists so that a path nobody thought to redact still cannot print a secret
// the process was handed.

// Secrets at or above this length are replaced wherever they occur. Shorter
// ones are replaced only as whole tokens, so a three-letter "secret" cannot
// turn every word containing it into a marker. Anything under three
// characters is not treated as a secret at all.
const (
	secretRegistryMinLen      = 3
	secretRegistrySubstringAt = 8
)

type registeredSecret struct {
	value   string
	escaped string // the value as encoding/json renders it, without quotes
	token   *regexp.Regexp
}

var (
	secretRegistryMu sync.RWMutex
	secretRegistry   []registeredSecret
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
			if existing.value == value {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		entry := registeredSecret{value: value}
		// JSON output escapes quotes, backslashes and HTML characters, so a
		// secret holding any of those never appears verbatim in --json
		// error output. Match the escaped rendering as well.
		if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
			if escaped := string(encoded[1 : len(encoded)-1]); escaped != value {
				entry.escaped = escaped
			}
		}
		if len(value) < secretRegistrySubstringAt {
			entry.token = regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(value) + `($|[^A-Za-z0-9_])`)
		}
		secretRegistry = append(secretRegistry, entry)
	}
	// Longest first, so a secret that contains another is replaced whole
	// instead of leaving fragments around a shorter match.
	sort.SliceStable(secretRegistry, func(i, j int) bool {
		return len(secretRegistry[i].value) > len(secretRegistry[j].value)
	})
}

// registerAuthorizationSecrets registers an Authorization-style value in every
// form a server might echo it: whole, payload only, and for Basic the decoded
// credentials and the password on its own.
func registerAuthorizationSecrets(value string) {
	registerSecret(authorizationSecretValues(value)...)
}

// registerCredentialsJSON registers a credentials file such as a GCS service
// account key: the whole document, and the private key on its own since that
// is the part a server would most plausibly echo.
func registerCredentialsJSON(data []byte) {
	registerSecret(strings.TrimSpace(string(data)))
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil {
		return
	}
	for _, name := range []string{"private_key", "private_key_id", "client_secret", "secret", "password", "token"} {
		if value, ok := fields[name].(string); ok {
			registerSecret(value)
		}
	}
}

// scrubKnownSecrets replaces every registered secret in text.
func scrubKnownSecrets(text string) string {
	secretRegistryMu.RLock()
	defer secretRegistryMu.RUnlock()
	for _, secret := range secretRegistry {
		if secret.token != nil {
			text = secret.token.ReplaceAllString(text, "${1}"+redactedMarker+"${2}")
		} else {
			text = strings.ReplaceAll(text, secret.value, redactedMarker)
		}
		if secret.escaped != "" {
			text = strings.ReplaceAll(text, secret.escaped, redactedMarker)
		}
	}
	return text
}

// scrubSecretsFromOutput prepares text for the terminal or a file: credential
// shapes are removed by pattern, then every registered secret by value.
func scrubSecretsFromOutput(text string) string {
	return scrubKnownSecrets(scrubCredentialText(text))
}

// resetSecretRegistryForTest clears the registry between tests.
func resetSecretRegistryForTest() {
	secretRegistryMu.Lock()
	defer secretRegistryMu.Unlock()
	secretRegistry = nil
}
