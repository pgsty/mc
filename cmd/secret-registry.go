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
	"sort"
	"strings"
	"sync"
)

// The secret registry is the last line of defense for output this client
// prints but did not compose: an error message an endpoint reflected a header
// into, a trace annotation that carried an argument, a report line. Every
// credential the process learns - alias secret keys and session tokens, SSE-C
// keys, passwords supplied on the command line - is registered as it is read,
// and final error output is scrubbed against the registry before it reaches
// the terminal or a file.
//
// Per-request redaction in the tracer stays the primary control. The registry
// exists so that a path nobody thought to redact still cannot print a secret
// the process was handed.

// secretRegistryMinLen keeps trivially short strings out of the registry.
// Anything shorter would turn scrubbing into a search-and-replace of common
// substrings; this client already rejects secret keys below eight characters.
const secretRegistryMinLen = 8

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
	// Longest first, so a secret that contains another is replaced whole
	// instead of leaving fragments around a shorter match.
	sort.Slice(secretRegistry, func(i, j int) bool {
		return len(secretRegistry[i]) > len(secretRegistry[j])
	})
}

// scrubKnownSecrets replaces every registered secret in text.
func scrubKnownSecrets(text string) string {
	secretRegistryMu.RLock()
	defer secretRegistryMu.RUnlock()
	for _, secret := range secretRegistry {
		text = strings.ReplaceAll(text, secret, redactedMarker)
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
