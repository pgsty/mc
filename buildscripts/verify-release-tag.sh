#!/usr/bin/env bash

# Verify that a release tag is annotated and carries a valid OpenPGP signature
# from the expected key, before anything is built from it.
#
# The expected fingerprint and the location of the public key are taken from
# the environment - repository variables in CI - rather than from the tree the
# tag points at, so the commit being released cannot vouch for itself. The
# imported key must itself have the expected fingerprint: a key URL that
# serves some other key is rejected even if that key signed the tag.
#
# Usage: verify-release-tag.sh TAG
#
# Environment:
#   RELEASE_SIGNING_FINGERPRINT   40 hex characters, required
#   RELEASE_SIGNING_KEY_URL       URL serving the ASCII-armored public key, required
#   TAG_VERIFY_GIT / TAG_VERIFY_GPG / TAG_VERIFY_CURL   executables (tests substitute fakes)

set -euo pipefail

tag="${1:-}"
fingerprint="${RELEASE_SIGNING_FINGERPRINT:-}"
key_url="${RELEASE_SIGNING_KEY_URL:-}"
git_bin="${TAG_VERIFY_GIT:-git}"
gpg_bin="${TAG_VERIFY_GPG:-gpg}"
curl_bin="${TAG_VERIFY_CURL:-curl}"

fail() {
  echo "verify-release-tag: $*" >&2
  exit 1
}

[[ "${tag}" =~ ^RELEASE\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}Z$ ]] || fail "invalid release tag: ${tag:-<empty>}"
fingerprint="$(echo "${fingerprint}" | tr -d ' ' | tr '[:lower:]' '[:upper:]')"
[[ "${fingerprint}" =~ ^[0-9A-F]{40}$ ]] || fail "RELEASE_SIGNING_FINGERPRINT must be a 40-character fingerprint"
[[ "${key_url}" =~ ^https:// ]] || fail "RELEASE_SIGNING_KEY_URL must be an https URL"

# Annotated, not lightweight: only an annotated tag can carry a signature.
object_type="$("${git_bin}" cat-file -t "${tag}" 2>/dev/null || true)"
[ "${object_type}" = "tag" ] || fail "${tag} is not an annotated tag (object type: ${object_type:-none})"

# A private keyring so the runner's keyring cannot contribute trust.
export GNUPGHOME
GNUPGHOME="$(mktemp -d)"
trap 'rm -rf "${GNUPGHOME}"' EXIT
chmod 700 "${GNUPGHOME}"

"${curl_bin}" -fsSL --max-time 30 "${key_url}" | "${gpg_bin}" --batch --quiet --import 2>/dev/null \
  || fail "unable to import the release signing key from ${key_url}"

if ! "${gpg_bin}" --batch --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" {print $10}' | grep -qx "${fingerprint}"; then
  fail "the key served by ${key_url} does not have fingerprint ${fingerprint}"
fi

# --raw prints gpg status lines: VALIDSIG carries the primary key fingerprint
# of the signing key as its tenth field (or the signing subkey's fingerprint
# first; the primary is the last field).
status="$("${git_bin}" verify-tag --raw "${tag}" 2>&1 || true)"
if ! grep -q '^\[GNUPG:\] GOODSIG ' <<<"${status}"; then
  echo "${status}" >&2
  fail "${tag} does not carry a good signature"
fi
signer="$(awk '/^\[GNUPG:\] VALIDSIG / {print toupper($NF)}' <<<"${status}" | head -n 1)"
[ "${signer}" = "${fingerprint}" ] || fail "${tag} was signed by ${signer:-an unknown key}, expected ${fingerprint}"

echo "verified ${tag}: annotated, signed by ${fingerprint}"
