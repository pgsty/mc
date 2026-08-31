#!/usr/bin/env bash

# Confirm an extracted binary is an ELF for the architecture its archive name
# claims, before it goes into an image. Reads the ELF header directly so the
# check does not depend on readelf or file being installed.
#
# Usage: verify-elf-arch.sh FILE amd64|arm64

set -euo pipefail

file="${1:-}"
arch="${2:-}"

fail() {
  echo "verify-elf-arch: $*" >&2
  exit 1
}

[ -f "${file}" ] || fail "missing file ${file:-<empty>}"

case "${arch}" in
  amd64) expected="003e" ;; # EM_X86_64
  arm64) expected="00b7" ;; # EM_AARCH64
  *) fail "unsupported architecture ${arch:-<empty>}" ;;
esac

# Bytes 0-3: magic. Byte 5: data encoding (1 = little-endian, which both
# targets use). Bytes 18-19: e_machine, little-endian.
header="$(od -An -tx1 -N 20 "${file}" | tr -d ' \n')"
[ "${#header}" -eq 40 ] || fail "${file} is too short to be an ELF binary"
[ "${header:0:8}" = "7f454c46" ] || fail "${file} is not an ELF binary"
[ "${header:10:2}" = "01" ] || fail "${file} is not little-endian"

machine="${header:38:2}${header:36:2}"
[ "${machine}" = "${expected}" ] || fail "${file} has ELF machine 0x${machine}, expected 0x${expected} for ${arch}"

echo "verified ${file} is an ELF binary for ${arch}"
