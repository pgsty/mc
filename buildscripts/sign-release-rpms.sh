#!/usr/bin/env bash

set -euo pipefail

expected_fingerprint="9592A7BC7A682E7333376E09E7935D8DB9BD8B20"
expected_vendor="PGSTY"
expected_packager="Ruohang Feng (@Vonng) <rh@vonng.com>"
expected_url="https://silo.pgsty.com"
expected_summary="Silo object storage client (mcli), a community-maintained fork of the MinIO Client."
expected_description="Silo object storage client (mcli), a community-maintained fork of the MinIO Client."
expected_license="AGPL-3.0-or-later"
expected_group="Applications/File"
repository="${GH_REPO:-pgsty/mc}"
container="${DNFUPDATE_CONTAINER:-dnfupdate}"
upload=false
release_tag=""

usage() {
  cat <<'EOF'
Usage: buildscripts/sign-release-rpms.sh RELEASE.TAG [--upload] [--repo OWNER/REPO] [--container NAME]

Downloads the two unsigned RPMs from a Draft GitHub Release, signs them with
the expected Pigsty key in the local dnfupdate container, verifies the result,
and regenerates their .sha256sum files. Nothing is uploaded unless --upload is
provided.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --upload)
      upload=true
      ;;
    --repo)
      shift
      if [ "$#" -eq 0 ]; then
        echo "--repo requires OWNER/REPO" >&2
        exit 1
      fi
      repository="$1"
      ;;
    --container)
      shift
      if [ "$#" -eq 0 ]; then
        echo "--container requires a name" >&2
        exit 1
      fi
      container="$1"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      if [ -n "${release_tag}" ]; then
        echo "Only one release tag may be specified" >&2
        exit 1
      fi
      release_tag="$1"
      ;;
  esac
  shift
done

if [ -z "${release_tag}" ]; then
  usage >&2
  exit 1
fi

for command in docker gh; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "${command} is required" >&2
    exit 1
  fi
done

version_hyphen="${release_tag#RELEASE.}"
package_version="$(printf '%s\n' "${version_hyphen}" | sed -E \
  's/^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2})-([0-9]{2})-([0-9]{2})Z$/\1\2\3\4\5\6.0.0/')"
if [ "${package_version}" = "${version_hyphen}" ]; then
  echo "Invalid release tag: ${release_tag}" >&2
  exit 1
fi

if [ "$(gh release view "${release_tag}" --repo "${repository}" --json isDraft --jq .isDraft)" != "true" ]; then
  echo "Refusing to sign: ${release_tag} is not a Draft release" >&2
  exit 1
fi

if [ "$(docker inspect --format '{{.State.Running}}' "${container}" 2>/dev/null || true)" != "true" ]; then
  echo "Signing container is not running: ${container}" >&2
  exit 1
fi

secret_fingerprints="$(docker exec "${container}" \
  gpg --batch --with-colons --list-secret-keys 2>/dev/null |
  awk -F: '$1 == "fpr" { print toupper($10) }')"
if ! printf '%s\n' "${secret_fingerprints}" | grep -Fxq "${expected_fingerprint}"; then
  echo "Expected signing key is not available in ${container}: ${expected_fingerprint}" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "${script_dir}/.." && pwd)"
work_root="${SIGN_WORKDIR:-${repo_dir}/.release-sign}"
mkdir -p "${work_root}"
work_dir="$(mktemp -d "${work_root}/${release_tag}.XXXXXX")"
unsigned_dir="${work_dir}/unsigned"
signed_dir="${work_dir}/signed"
mkdir -p "${unsigned_dir}" "${signed_dir}"
chmod 700 "${work_dir}" "${unsigned_dir}" "${signed_dir}"

rpm_files=(
  "mcli-${package_version}-1.x86_64.rpm"
  "mcli-${package_version}-1.aarch64.rpm"
)

download_patterns=()
for rpm_file in "${rpm_files[@]}"; do
  download_patterns+=(--pattern "${rpm_file}" --pattern "${rpm_file}.sha256sum")
done

echo "Downloading RPMs from Draft release ${repository}@${release_tag}"
gh release download "${release_tag}" --repo "${repository}" \
  --dir "${unsigned_dir}" "${download_patterns[@]}"

sha256_digest() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
  else
    shasum -a 256 "${file}" | awk '{print $1}'
  fi
}

for rpm_file in "${rpm_files[@]}"; do
  rpm_path="${unsigned_dir}/${rpm_file}"
  checksum_path="${rpm_path}.sha256sum"
  test -s "${rpm_path}"
  test -s "${checksum_path}"

  actual_line="$(sha256_digest "${rpm_path}")  ${rpm_file}"
  published_line="$(tr -d '\n' < "${checksum_path}")"
  if [ "${actual_line}" != "${published_line}" ]; then
    echo "Checksum mismatch for ${rpm_file}" >&2
    exit 1
  fi
done

safe_tag="$(printf '%s' "${release_tag}" | tr -c 'A-Za-z0-9._-' '_')"
container_dir="/tmp/mcli-sign-${safe_tag}-$$"
docker exec "${container}" mkdir -p "${container_dir}"

cleanup_container() {
  local rpm_file
  for rpm_file in "${rpm_files[@]}"; do
    docker exec "${container}" rm -f "${container_dir}/${rpm_file}" >/dev/null 2>&1 || true
  done
  docker exec "${container}" rmdir "${container_dir}" >/dev/null 2>&1 || true
}
trap cleanup_container EXIT

assert_rpm_tag() {
  local rpm_path="$1"
  local tag="$2"
  local expected="$3"
  local actual

  actual="$(docker exec "${container}" rpm -qp --queryformat "%{${tag}}" "${rpm_path}")"
  if [ "${actual}" != "${expected}" ]; then
    echo "Unexpected RPM ${tag}: ${actual}" >&2
    echo "Expected RPM ${tag}: ${expected}" >&2
    exit 1
  fi
}

for rpm_file in "${rpm_files[@]}"; do
  case "${rpm_file}" in
    *.x86_64.rpm)
      expected_arch="x86_64"
      ;;
    *.aarch64.rpm)
      expected_arch="aarch64"
      ;;
    *)
      echo "Unexpected RPM filename: ${rpm_file}" >&2
      exit 1
      ;;
  esac

  echo "Signing ${rpm_file} with ${expected_fingerprint}"
  docker cp "${unsigned_dir}/${rpm_file}" "${container}:${container_dir}/${rpm_file}" >/dev/null
  container_rpm="${container_dir}/${rpm_file}"

  assert_rpm_tag "${container_rpm}" NAME mcli
  assert_rpm_tag "${container_rpm}" VERSION "${package_version}"
  assert_rpm_tag "${container_rpm}" RELEASE 1
  assert_rpm_tag "${container_rpm}" ARCH "${expected_arch}"
  assert_rpm_tag "${container_rpm}" VENDOR "${expected_vendor}"
  assert_rpm_tag "${container_rpm}" PACKAGER "${expected_packager}"
  assert_rpm_tag "${container_rpm}" URL "${expected_url}"
  assert_rpm_tag "${container_rpm}" LICENSE "${expected_license}"
  assert_rpm_tag "${container_rpm}" GROUP "${expected_group}"
  assert_rpm_tag "${container_rpm}" SUMMARY "${expected_summary}"
  assert_rpm_tag "${container_rpm}" DESCRIPTION "${expected_description}"

  docker exec "${container}" rpm -qpl "${container_rpm}" |
    "${repo_dir}/buildscripts/verify-rpm-payload.sh" "${rpm_file}"

  docker exec "${container}" rpmsign \
    --define "_gpg_name ${expected_fingerprint}" \
    --addsign "${container_rpm}"

  signature_output="$(docker exec "${container}" rpmkeys --checksig --verbose "${container_rpm}")"
  printf '%s\n' "${signature_output}"
  if ! printf '%s\n' "${signature_output}" | tr '[:upper:]' '[:lower:]' | grep -q 'key id b9bd8b20: ok'; then
    echo "Signature verification failed for ${rpm_file}" >&2
    exit 1
  fi

  docker cp "${container}:${container_dir}/${rpm_file}" "${signed_dir}/${rpm_file}" >/dev/null
  signed_digest="$(sha256_digest "${signed_dir}/${rpm_file}")"
  printf '%s  %s' "${signed_digest}" "${rpm_file}" > "${signed_dir}/${rpm_file}.sha256sum"

  docker exec "${container}" rpm -qp --queryformat \
    $'Name: %{NAME}\nVersion: %{VERSION}-%{RELEASE}\nArch: %{ARCH}\nVendor: %{VENDOR}\nPackager: %{PACKAGER}\nURL: %{URL}\n' \
    "${container_rpm}"
  echo "SHA256: ${signed_digest}"
done

if [ "${upload}" = true ]; then
  upload_files=()
  for rpm_file in "${rpm_files[@]}"; do
    upload_files+=("${signed_dir}/${rpm_file}" "${signed_dir}/${rpm_file}.sha256sum")
  done

  echo "Replacing RPMs in Draft release ${release_tag}"
  gh release upload "${release_tag}" --repo "${repository}" --clobber "${upload_files[@]}"

  for rpm_file in "${rpm_files[@]}"; do
    for asset in "${rpm_file}" "${rpm_file}.sha256sum"; do
      local_digest="sha256:$(sha256_digest "${signed_dir}/${asset}")"
      remote_digest=""
      for attempt in 1 2 3 4 5; do
        remote_digest="$(gh release view "${release_tag}" --repo "${repository}" --json assets \
          --jq ".assets[] | select(.name == \"${asset}\") | .digest")"
        if [ "${local_digest}" = "${remote_digest}" ]; then
          break
        fi
        if [ "${attempt}" -lt 5 ]; then
          sleep 2
        fi
      done
      if [ "${local_digest}" != "${remote_digest}" ]; then
        echo "GitHub asset digest mismatch for ${asset}" >&2
        echo "Local:  ${local_digest}" >&2
        echo "Remote: ${remote_digest}" >&2
        exit 1
      fi
      echo "Verified GitHub asset: ${asset} ${remote_digest}"
    done
  done
else
  echo
  echo "Signed RPMs are ready for review in: ${signed_dir}"
  echo "Re-run with --upload to replace the RPM assets in the Draft release."
fi
