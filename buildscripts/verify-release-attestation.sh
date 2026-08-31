#!/usr/bin/env bash

# Verify that a downloaded release asset is exactly the file the Release
# workflow attested for this tag: same provenance workflow identity, same
# source ref and commit, same subject name, same SHA-256, built on a
# GitHub-hosted runner.
#
# Checking the asset against the checksums file from the same Release proves
# only that the two agree with each other. The attestation is what binds the
# bytes to the workflow run that produced them; this script binds the run to
# the tag and commit being published, and the subject to the filename, so a
# valid arm64 archive cannot be renamed as amd64, nor an attested archive from
# another tag be re-uploaded here.
#
# Usage: verify-release-attestation.sh TAG COMMIT FILE...
#
# Environment:
#   GITHUB_REPOSITORY       owner/repo (default pgsty/mc)
#   ATTESTATION_GH          gh executable (default gh); tests substitute a fake
#   ATTESTATION_MIN_GH      minimum gh version accepted (default 2.66.0)

set -euo pipefail

tag="${1:-}"
commit="${2:-}"
shift 2 2>/dev/null || true
repository="${GITHUB_REPOSITORY:-pgsty/mc}"
gh_bin="${ATTESTATION_GH:-gh}"
min_gh="${ATTESTATION_MIN_GH:-2.66.0}"

fail() {
  echo "verify-release-attestation: $*" >&2
  exit 1
}

[[ "${tag}" =~ ^RELEASE\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}Z$ ]] || fail "invalid release tag: ${tag:-<empty>}"
[[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || fail "invalid release commit: ${commit:-<empty>}"
[ "$#" -ge 1 ] || fail "no files to verify"
command -v jq >/dev/null 2>&1 || fail "jq is required"

# --source-ref, --source-digest and --deny-self-hosted-runners need a gh that
# knows them; an older gh would silently accept a policy it cannot enforce.
gh_version="$("${gh_bin}" --version 2>/dev/null | sed -nE 's/^gh version ([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -n 1)"
[ -n "${gh_version}" ] || fail "unable to read gh version"
if [ "$(printf '%s\n%s\n' "${min_gh}" "${gh_version}" | sort -V | head -n 1)" != "${min_gh}" ]; then
  fail "gh ${gh_version} is older than the required ${min_gh}"
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

identity="https://github.com/${repository}/.github/workflows/release.yml@refs/tags/${tag}"

for file in "$@"; do
  [ -f "${file}" ] || fail "missing file ${file}"
  name="$(basename "${file}")"
  digest="$(sha256_of "${file}")"

  if ! result="$("${gh_bin}" attestation verify "${file}" \
      --repo "${repository}" \
      --cert-identity "${identity}" \
      --source-ref "refs/tags/${tag}" \
      --source-digest "${commit}" \
      --deny-self-hosted-runners \
      --predicate-type "https://slsa.dev/provenance/v1" \
      --format json 2>&1)"; then
    echo "${result}" >&2
    fail "attestation verification failed for ${name}"
  fi

  # gh returns one entry per matching attestation. A retried release can
  # legitimately attest the same bytes twice, so more than one is allowed -
  # but every one of them must name this file with this digest.
  count="$(jq 'if type == "array" then length else -1 end' <<<"${result}" 2>/dev/null || echo -1)"
  [ "${count}" -ge 1 ] || fail "no verified attestation returned for ${name}"

  matching="$(jq --arg name "${name}" --arg digest "${digest}" '
    [ .[] | .verificationResult.statement.subject
      | select(type == "array")
      | select(any(.[]; .name == $name and .digest.sha256 == $digest)) ] | length' <<<"${result}")"
  [ "${matching}" -eq "${count}" ] || fail "attested subject does not match ${name} sha256:${digest} (${matching}/${count} attestations agree)"

  echo "verified ${name} sha256:${digest} against ${count} attestation(s) from ${identity}"
done
