#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="${script_dir}/check-release-state.sh"
tag="RELEASE.2026-08-29T00-00-00Z"
fixture="$(mktemp)"
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
trap 'rm -f "${fixture}" "${stdout_file}" "${stderr_file}"' EXIT

expect_success() {
  if ! "${checker}" "$@" >"${stdout_file}" 2>"${stderr_file}"; then
    cat "${stderr_file}" >&2
    return 1
  fi
}

expect_failure() {
  if "${checker}" "$@" >"${stdout_file}" 2>"${stderr_file}"; then
    echo "Expected release-state check to fail: $*" >&2
    return 1
  fi
}

printf '[]\n' >"${fixture}"
expect_success "${tag}" "${fixture}"
grep -qF "No existing release for ${tag}." "${stdout_file}"

if REQUIRE_DRAFT=true "${checker}" "${tag}" "${fixture}" >"${stdout_file}" 2>"${stderr_file}"; then
  echo "Expected required-Draft check to fail when no release exists" >&2
  exit 1
fi
grep -qF "Expected one Draft release for ${tag}, found none" "${stderr_file}"

printf '[{"tag_name":"%s","draft":true}]\n' "${tag}" >"${fixture}"
expect_success "${tag}" "${fixture}"
grep -qF "Existing Draft ${tag} will be replaced from scratch." "${stdout_file}"
if ! REQUIRE_DRAFT=true "${checker}" "${tag}" "${fixture}" >"${stdout_file}" 2>"${stderr_file}"; then
  cat "${stderr_file}" >&2
  exit 1
fi
grep -qF "Existing Draft ${tag} will be replaced from scratch." "${stdout_file}"

printf '[{"tag_name":"%s","draft":false}]\n' "${tag}" >"${fixture}"
expect_failure "${tag}" "${fixture}"
grep -qF "Refusing to overwrite published release ${tag}" "${stderr_file}"
if REQUIRE_DRAFT=true "${checker}" "${tag}" "${fixture}" >"${stdout_file}" 2>"${stderr_file}"; then
  echo "Expected required-Draft check to reject a published release" >&2
  exit 1
fi
grep -qF "Refusing to overwrite published release ${tag}" "${stderr_file}"

printf '[{"tag_name":"%s","draft":true},{"tag_name":"%s","draft":true}]\n' "${tag}" "${tag}" >"${fixture}"
expect_failure "${tag}" "${fixture}"
grep -qF "Refusing to choose among 2 releases" "${stderr_file}"

printf '[{"tag_name":"RELEASE.2026-08-28T00-00-00Z","draft":true}]\n' >"${fixture}"
expect_failure "${tag}" "${fixture}"
grep -qF "other than ${tag}" "${stderr_file}"

printf '{not-json}\n' >"${fixture}"
expect_failure "${tag}" "${fixture}"
grep -qF "Invalid release state response for ${tag}" "${stderr_file}"

expect_failure "not-a-release-tag" "${fixture}"
grep -qF "Invalid release tag format" "${stderr_file}"

echo "release-state decision tests passed"
