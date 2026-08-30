#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="${script_dir}/check-release-commit.sh"
commit="0123456789abcdef0123456789abcdef01234567"
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
    echo "Expected release-commit check to fail: $*" >&2
    return 1
  fi
}

green_run() {
  printf '{"name":"%s","status":"completed","conclusion":"success"}' "$1"
}

# All three required workflows green.
printf '[%s,%s,%s]\n' "$(green_run Go)" "$(green_run Crosscompile)" "$(green_run VulnCheck)" >"${fixture}"
expect_success "${commit}" "${fixture}"
grep -qF "Required workflows are green for ${commit}." "${stdout_file}"

# A path-filtered workflow with no run must not block the release.
printf '[%s,%s,%s]\n' "$(green_run Go)" "$(green_run Crosscompile)" "$(green_run VulnCheck)" >"${fixture}"
expect_success "${commit}" "${fixture}"

# A re-run that finally succeeded is enough; the earlier failure is history.
printf '[{"name":"Go","status":"completed","conclusion":"failure"},%s,%s,%s]\n' \
  "$(green_run Go)" "$(green_run Crosscompile)" "$(green_run VulnCheck)" >"${fixture}"
expect_success "${commit}" "${fixture}"

# Missing workflow.
printf '[%s,%s]\n' "$(green_run Go)" "$(green_run Crosscompile)" >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "Required workflow VulnCheck has no successful run" "${stderr_file}"
grep -qF "(no run)" "${stderr_file}"

# Still running.
printf '[%s,%s,{"name":"VulnCheck","status":"in_progress","conclusion":null}]\n' \
  "$(green_run Go)" "$(green_run Crosscompile)" >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "Required workflow VulnCheck has no successful run" "${stderr_file}"
grep -qF "(in_progress)" "${stderr_file}"

# Failed outright.
printf '[%s,%s,{"name":"VulnCheck","status":"completed","conclusion":"failure"}]\n' \
  "$(green_run Go)" "$(green_run Crosscompile)" >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "(failure)" "${stderr_file}"

# Cancelled counts as not green.
printf '[%s,%s,{"name":"VulnCheck","status":"completed","conclusion":"cancelled"}]\n' \
  "$(green_run Go)" "$(green_run Crosscompile)" >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "(cancelled)" "${stderr_file}"

# A green run of some other workflow must not satisfy a required one.
printf '[%s,%s,%s]\n' "$(green_run Go)" "$(green_run Crosscompile)" "$(green_run 'Publish Docker Image')" >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "Required workflow VulnCheck has no successful run" "${stderr_file}"

# Malformed API response.
printf '{not-json}\n' >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "Invalid workflow run response for ${commit}" "${stderr_file}"

# An empty array is a valid response that satisfies nothing.
printf '[]\n' >"${fixture}"
expect_failure "${commit}" "${fixture}"
grep -qF "Required workflow Go has no successful run" "${stderr_file}"

expect_failure "not-a-commit" "${fixture}"
grep -qF "Invalid release commit" "${stderr_file}"

expect_failure "" "${fixture}"
grep -qF "Invalid release commit: <empty>" "${stderr_file}"

echo "release-commit decision tests passed"
