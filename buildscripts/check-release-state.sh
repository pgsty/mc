#!/usr/bin/env bash

# Fail closed before a release job can replace published assets. Draft releases
# are replaceable retry state; published releases are immutable.

set -euo pipefail

release_tag="${1:-}"
fixture="${2:-}"
repository="${GITHUB_REPOSITORY:-pgsty/mc}"
require_draft="${REQUIRE_DRAFT:-false}"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to inspect GitHub release state" >&2
  exit 1
fi

if [[ ! "${release_tag}" =~ ^RELEASE\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}Z$ ]]; then
  echo "Invalid release tag format: ${release_tag:-<empty>}" >&2
  exit 1
fi

if [ -n "${fixture}" ]; then
  release_json="$(cat "${fixture}")"
else
  error_file="$(mktemp)"
  trap 'rm -f "${error_file}"' EXIT
  if ! release_json="$(
    gh api --paginate "repos/${repository}/releases?per_page=100" --jq '.[]' 2>"${error_file}" |
      jq --arg tag "${release_tag}" -s '[.[] | select(.tag_name == $tag)]'
  )"; then
    cat "${error_file}" >&2
    exit 1
  fi
fi

if ! jq -e 'type == "array" and all(.[]; type == "object" and (.tag_name | type == "string") and (.draft | type == "boolean"))' \
  <<<"${release_json}" >/dev/null 2>&1; then
  echo "Invalid release state response for ${release_tag}" >&2
  exit 1
fi

if ! jq -e --arg tag "${release_tag}" 'all(.[]; .tag_name == $tag)' \
  <<<"${release_json}" >/dev/null 2>&1; then
  echo "Release state returned a tag other than ${release_tag}" >&2
  exit 1
fi

release_count="$(jq 'length' <<<"${release_json}")"
if [ "${release_count}" -eq 0 ]; then
  if [ "${require_draft}" = "true" ]; then
    echo "Expected one Draft release for ${release_tag}, found none" >&2
    exit 1
  fi
  echo "No existing release for ${release_tag}."
  exit 0
fi

if [ "${release_count}" -ne 1 ]; then
  echo "Refusing to choose among ${release_count} releases for ${release_tag}; clean duplicate Drafts first" >&2
  exit 1
fi

if [ "$(jq -r '.[0].draft' <<<"${release_json}")" != "true" ]; then
  echo "Refusing to overwrite published release ${release_tag}" >&2
  exit 1
fi

echo "Existing Draft ${release_tag} will be replaced from scratch."
