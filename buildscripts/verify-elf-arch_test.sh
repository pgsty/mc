#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="${script_dir}/verify-elf-arch.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

# elf FILE MACHINE_LE_HEX [DATA_ENCODING]: a 20-byte ELF header stub.
elf() {
  local file="$1" machine="$2" encoding="${3:-01}"
  printf '\x7fELF\x02%b\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00%b' \
    "\\x${encoding}" "\\x${machine:0:2}\\x${machine:2:2}" >"${file}"
}

elf "${work}/amd64" "3e00"
elf "${work}/arm64" "b700"
elf "${work}/bigendian" "00b7" "02"
printf 'not an elf at all, just text' >"${work}/text"
printf '\x7fELF' >"${work}/short"

"${checker}" "${work}/amd64" amd64 >/dev/null
"${checker}" "${work}/arm64" arm64 >/dev/null

expect_failure() {
  if "${checker}" "$@" >/dev/null 2>"${work}/err"; then
    echo "expected verify-elf-arch to fail: $*" >&2
    return 1
  fi
}

# The swap that a renamed archive would produce.
expect_failure "${work}/amd64" arm64
grep -qF "expected 0x00b7 for arm64" "${work}/err"
expect_failure "${work}/arm64" amd64
grep -qF "expected 0x003e for amd64" "${work}/err"

expect_failure "${work}/bigendian" arm64
grep -qF "not little-endian" "${work}/err"
expect_failure "${work}/text" amd64
grep -qF "not an ELF binary" "${work}/err"
expect_failure "${work}/short" amd64
grep -qF "too short" "${work}/err"
expect_failure "${work}/amd64" riscv64
expect_failure "${work}/missing" amd64

echo "elf-arch decision tests passed"
