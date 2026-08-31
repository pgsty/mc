#!/usr/bin/env bash

# Fixture tests for verify-release-tag.sh. Fake git, gpg and curl replay the
# outputs of each situation so every decision is pinned without a keyring.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="${script_dir}/verify-release-tag.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

tag="RELEASE.2026-08-31T00-00-00Z"
fpr="9592A7BC7A682E7333376E09E7935D8DB9BD8B20"
other="0000000000000000000000000000000000000000"

cat >"${work}/git" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  cat-file) printf '%s\n' "${FAKE_OBJECT_TYPE:-tag}" ;;
  verify-tag) printf '%b' "${FAKE_VERIFY_STATUS}" ;;
  *) exit 2 ;;
esac
EOF
cat >"${work}/gpg" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *--import*) cat >/dev/null; exit "${FAKE_IMPORT_RC:-0}" ;;
  *--fingerprint*) printf 'fpr:::::::::%s:\n' "${FAKE_KEY_FPR}" ;;
  *) exit 2 ;;
esac
EOF
cat >"${work}/curl" <<'EOF'
#!/usr/bin/env bash
echo "-----BEGIN PGP PUBLIC KEY BLOCK-----"
EOF
chmod +x "${work}/git" "${work}/gpg" "${work}/curl"
export TAG_VERIFY_GIT="${work}/git" TAG_VERIFY_GPG="${work}/gpg" TAG_VERIFY_CURL="${work}/curl"
export RELEASE_SIGNING_FINGERPRINT="${fpr}" RELEASE_SIGNING_KEY_URL="https://github.com/example.gpg"
export FAKE_KEY_FPR="${fpr}"
good_status="[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG E7935D8DB9BD8B20 Example <e@example>\n[GNUPG:] VALIDSIG ${fpr} 2026-08-31 1787765892 0 4 0 1 10 00 ${fpr}\n"
export FAKE_VERIFY_STATUS="${good_status}"

expect_success() {
  if ! "${checker}" "$@" >"${work}/out" 2>"${work}/err"; then
    cat "${work}/err" >&2
    return 1
  fi
}
expect_failure() {
  if "${checker}" "$@" >"${work}/out" 2>"${work}/err"; then
    echo "expected verify-release-tag to fail: $* ($(env | grep FAKE_ | tr '\n' ' '))" >&2
    return 1
  fi
}

expect_success "${tag}"
grep -qF "signed by ${fpr}" "${work}/out"

# Lowercase / spaced fingerprint variable is normalized.
RELEASE_SIGNING_FINGERPRINT="9592 a7bc 7a68 2e73 3337 6e09 e793 5d8d b9bd 8b20" expect_success "${tag}"

# Lightweight tag.
FAKE_OBJECT_TYPE=commit expect_failure "${tag}"
grep -qF "not an annotated tag" "${work}/err"

# Signed by a different key.
FAKE_VERIFY_STATUS="[GNUPG:] GOODSIG 0000000000000000 Other <o@example>\n[GNUPG:] VALIDSIG ${other} 2026-08-31 1 0 4 0 1 10 00 ${other}\n" expect_failure "${tag}"
grep -qF "expected ${fpr}" "${work}/err"

# Unsigned or bad signature.
FAKE_VERIFY_STATUS="error: no signature found\n" expect_failure "${tag}"
grep -qF "does not carry a good signature" "${work}/err"
FAKE_VERIFY_STATUS="[GNUPG:] BADSIG E7935D8DB9BD8B20 Example\n" expect_failure "${tag}"

# The key URL serves a different key than the expected fingerprint.
FAKE_KEY_FPR="${other}" expect_failure "${tag}"
grep -qF "does not have fingerprint" "${work}/err"

# Key import fails.
FAKE_IMPORT_RC=1 expect_failure "${tag}"
grep -qF "unable to import" "${work}/err"

# Configuration must be present and well-formed.
RELEASE_SIGNING_FINGERPRINT="" expect_failure "${tag}"
RELEASE_SIGNING_FINGERPRINT="short" expect_failure "${tag}"
RELEASE_SIGNING_KEY_URL="http://insecure/key" expect_failure "${tag}"
expect_failure "not-a-tag"

echo "release-tag decision tests passed"
