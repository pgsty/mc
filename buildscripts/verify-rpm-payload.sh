#!/usr/bin/env bash

set -euo pipefail

# Validate the complete RPM payload shared by release CI and the maintainer
# signing workflow. Read the paths produced by `rpm -qpl` from standard input.

package_name="${1:-RPM package}"

expected_payload="$({
  printf '%s\n' \
    '/usr/local/bin/mcli' \
    '/usr/share/doc/mcli/CREDITS' \
    '/usr/share/doc/mcli/NOTICE' \
    '/usr/share/licenses/mcli/LICENSE'
} | LC_ALL=C sort)"
actual_payload="$(LC_ALL=C sort)"

if [ "${actual_payload}" != "${expected_payload}" ]; then
  echo "Unexpected RPM payload for ${package_name}:" >&2
  printf '%s\n' "${actual_payload}" >&2
  echo "Expected RPM payload:" >&2
  printf '%s\n' "${expected_payload}" >&2
  exit 1
fi
