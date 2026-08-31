#!/usr/bin/env bash

# Fixture tests for verify-release-attestation.sh. A fake gh records the exact
# arguments it was called with and replies with a canned verification result,
# so both the policy the script asks gh to enforce and the decisions it makes
# on gh's answer are pinned.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="${script_dir}/verify-release-attestation.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

tag="RELEASE.2026-08-31T00-00-00Z"
commit="0123456789abcdef0123456789abcdef01234567"
asset="${work}/mcli_20260831000000.0.0_linux_amd64.tar.gz"
printf 'release bytes\n' >"${asset}"
if command -v sha256sum >/dev/null 2>&1; then
  digest="$(sha256sum "${asset}" | awk '{print $1}')"
else
  digest="$(shasum -a 256 "${asset}" | awk '{print $1}')"
fi

# The fake gh: prints its arguments to ARGS_FILE, then emits RESPONSE_FILE.
fake_gh="${work}/gh"
cat >"${fake_gh}" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = "--version" ]; then
  echo "gh version ${FAKE_GH_VERSION:-2.96.0} (2026-07-02)"
  exit 0
fi
printf '%s\n' "$@" >"${ARGS_FILE}"
if [ -n "${FAKE_GH_FAIL:-}" ]; then
  echo "verification failed: ${FAKE_GH_FAIL}" >&2
  exit 1
fi
cat "${RESPONSE_FILE}"
EOF
chmod +x "${fake_gh}"
export ATTESTATION_GH="${fake_gh}" ARGS_FILE="${work}/args" RESPONSE_FILE="${work}/response"
export GITHUB_REPOSITORY="pgsty/mc"

attestation() {
  # attestation NAME DIGEST -> one verification result
  printf '{"verificationResult":{"statement":{"subject":[{"name":"%s","digest":{"sha256":"%s"}}]}}}' "$1" "$2"
}

expect_success() {
  if ! "${checker}" "$@" >"${work}/out" 2>"${work}/err"; then
    cat "${work}/err" >&2
    return 1
  fi
}

expect_failure() {
  if "${checker}" "$@" >"${work}/out" 2>"${work}/err"; then
    echo "expected verify-release-attestation to fail: $*" >&2
    cat "${work}/out" >&2
    return 1
  fi
}

# --- happy path, and the exact policy handed to gh ---
printf '[%s]\n' "$(attestation "$(basename "${asset}")" "${digest}")" >"${RESPONSE_FILE}"
expect_success "${tag}" "${commit}" "${asset}"
grep -qF "verified $(basename "${asset}") sha256:${digest}" "${work}/out"

expected_args="attestation
verify
${asset}
--repo
pgsty/mc
--cert-identity
https://github.com/pgsty/mc/.github/workflows/release.yml@refs/tags/${tag}
--source-ref
refs/tags/${tag}
--source-digest
${commit}
--deny-self-hosted-runners
--predicate-type
https://slsa.dev/provenance/v1
--format
json"
if [ "$(cat "${ARGS_FILE}")" != "${expected_args}" ]; then
  echo "gh was called with unexpected arguments:" >&2
  diff <(printf '%s\n' "${expected_args}") "${ARGS_FILE}" >&2 || true
  exit 1
fi

# Two attestations for the same bytes (a retried release) are fine.
printf '[%s,%s]\n' "$(attestation "$(basename "${asset}")" "${digest}")" "$(attestation "$(basename "${asset}")" "${digest}")" >"${RESPONSE_FILE}"
expect_success "${tag}" "${commit}" "${asset}"
grep -qF "against 2 attestation(s)" "${work}/out"

# --- the decisions on gh's answer ---
# Right digest under the wrong filename: the amd64 archive renamed from arm64.
printf '[%s]\n' "$(attestation "mcli_20260831000000.0.0_linux_arm64.tar.gz" "${digest}")" >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "attested subject does not match" "${work}/err"

# Right filename, different bytes.
printf '[%s]\n' "$(attestation "$(basename "${asset}")" "0000000000000000000000000000000000000000000000000000000000000000")" >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "attested subject does not match" "${work}/err"

# One matching and one foreign attestation: every attestation must agree.
printf '[%s,%s]\n' "$(attestation "$(basename "${asset}")" "${digest}")" "$(attestation "other.tar.gz" "${digest}")" >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "(1/2 attestations agree)" "${work}/err"

# The Release workflow attests all release files in one multi-subject
# statement. It is valid when this file's exact name+digest pair is present.
printf '[{"verificationResult":{"statement":{"subject":[{"name":"%s","digest":{"sha256":"%s"}},{"name":"x","digest":{"sha256":"%s"}}]}}}]\n' \
  "$(basename "${asset}")" "${digest}" "${digest}" >"${RESPONSE_FILE}"
expect_success "${tag}" "${commit}" "${asset}"

# A multi-subject statement with the right name and digest only on different
# subjects must fail: an arm64 archive renamed as amd64 has this shape.
printf '[{"verificationResult":{"statement":{"subject":[{"name":"%s","digest":{"sha256":"%s"}},{"name":"other.tar.gz","digest":{"sha256":"%s"}}]}}}]\n' \
  "$(basename "${asset}")" "0000000000000000000000000000000000000000000000000000000000000000" "${digest}" >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "attested subject does not match" "${work}/err"

# Empty, malformed, or not an array.
printf '[]\n' >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "no verified attestation returned" "${work}/err"
printf '{not-json}\n' >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"
printf '{"verificationResult":{}}\n' >"${RESPONSE_FILE}"
expect_failure "${tag}" "${commit}" "${asset}"

# gh itself rejects the policy (wrong signer, wrong ref, self-hosted runner...).
printf '[%s]\n' "$(attestation "$(basename "${asset}")" "${digest}")" >"${RESPONSE_FILE}"
FAKE_GH_FAIL="certificate identity mismatch" expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "certificate identity mismatch" "${work}/err"
grep -qF "attestation verification failed" "${work}/err"

# A gh too old to enforce the policy is refused before any call is made.
rm -f "${ARGS_FILE}"
FAKE_GH_VERSION="2.50.0" expect_failure "${tag}" "${commit}" "${asset}"
grep -qF "older than the required" "${work}/err"
[ ! -e "${ARGS_FILE}" ]

# Argument validation.
expect_failure "not-a-tag" "${commit}" "${asset}"
expect_failure "${tag}" "short" "${asset}"
expect_failure "${tag}" "${commit}"
expect_failure "${tag}" "${commit}" "${work}/does-not-exist"

echo "release-attestation decision tests passed"
