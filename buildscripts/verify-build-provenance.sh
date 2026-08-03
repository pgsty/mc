#!/usr/bin/env bash

set -euo pipefail

# Asserts that every binary GoReleaser produced is stamped by the Go toolchain
# as built from this exact commit with a clean working tree. A stray untracked
# file (for example an un-ignored dist/) silently turns every release binary
# into a "+dirty" pseudo-version, which destroys the link between a published
# artifact and its tag. Catch that here instead of after publishing.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "${script_dir}/.." && pwd)"
dist_dir="${DIST_DIR:-${repo_dir}/dist}"
expected_count="${EXPECTED_BINARY_COUNT:-6}"

if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 1
fi

if [ ! -d "${dist_dir}" ]; then
  echo "Missing GoReleaser dist directory: ${dist_dir}" >&2
  exit 1
fi

revision="$(git -C "${repo_dir}" rev-parse HEAD)"

count=0
while IFS= read -r binary; do
  count=$((count + 1))
  info="$(go version -m "${binary}")"

  if ! grep -qF "vcs.revision=${revision}" <<< "${info}"; then
    echo "Unexpected vcs.revision in ${binary} (expected ${revision})" >&2
    grep -F 'vcs.' <<< "${info}" >&2 || true
    exit 1
  fi

  if ! grep -qF 'vcs.modified=false' <<< "${info}"; then
    echo "Binary was built from a dirty working tree: ${binary}" >&2
    grep -F 'vcs.' <<< "${info}" >&2 || true
    exit 1
  fi
done < <(find "${dist_dir}" -maxdepth 2 -type f \( -name 'mcli' -o -name 'mcli.exe' \) | sort)

if [ "${count}" -ne "${expected_count}" ]; then
  echo "Expected ${expected_count} release binaries, found ${count}" >&2
  exit 1
fi

echo "Verified ${count} binaries built from ${revision} with a clean tree"
