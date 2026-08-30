#!/usr/bin/env bash

# Fail closed before a release job builds a commit that main never accepted or
# that CI never validated.
#
# check-release-state.sh answers "is this tag safe to write to". This answers
# the other half: "is this commit safe to ship". The release workflow already
# proves the tag resolves to the checkout, but a tag can be pushed to any
# commit at any time, including one that never landed on main and one whose
# tests have not finished.

set -euo pipefail

release_commit="${1:-}"
fixture="${2:-}"
repository="${GITHUB_REPOSITORY:-pgsty/mc}"
release_branch="${RELEASE_BRANCH:-origin/main}"

# Workflows that must have concluded successfully for this exact commit.
# Test Release Pipeline is deliberately absent: it is path-filtered, so a
# release commit that touches no packaging file legitimately has no run.
required_workflows=("Go" "Crosscompile" "VulnCheck")

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to inspect workflow runs" >&2
  exit 1
fi

if [[ ! "${release_commit}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Invalid release commit: ${release_commit:-<empty>}" >&2
  exit 1
fi

if [ -z "${fixture}" ]; then
  if ! git rev-parse --verify --quiet "${release_branch}" >/dev/null; then
    echo "Cannot resolve ${release_branch}; fetch it before releasing" >&2
    exit 1
  fi
  if ! git merge-base --is-ancestor "${release_commit}" "${release_branch}"; then
    echo "Refusing to release ${release_commit}: it is not an ancestor of ${release_branch}" >&2
    exit 1
  fi
  echo "Release commit ${release_commit} is on ${release_branch}."

  error_file="$(mktemp)"
  trap 'rm -f "${error_file}"' EXIT
  if ! runs_json="$(
    gh api --paginate "repos/${repository}/actions/runs?head_sha=${release_commit}&per_page=100" \
      --jq '.workflow_runs[] | {name, status, conclusion}' 2>"${error_file}" |
      jq -s '.'
  )"; then
    cat "${error_file}" >&2
    exit 1
  fi
else
  runs_json="$(cat "${fixture}")"
fi

if ! jq -e 'type == "array" and all(.[]; type == "object" and (.name | type == "string"))' \
  <<<"${runs_json}" >/dev/null 2>&1; then
  echo "Invalid workflow run response for ${release_commit}" >&2
  exit 1
fi

fail=0
for workflow in "${required_workflows[@]}"; do
  succeeded="$(jq --arg name "${workflow}" \
    '[.[] | select(.name == $name and .status == "completed" and .conclusion == "success")] | length' \
    <<<"${runs_json}")"
  if [ "${succeeded}" -eq 0 ]; then
    observed="$(jq -r --arg name "${workflow}" \
      '[.[] | select(.name == $name) | (.conclusion // .status)] | if length == 0 then "no run" else join(", ") end' \
      <<<"${runs_json}")"
    echo "Required workflow ${workflow} has no successful run for ${release_commit} (${observed})" >&2
    fail=1
  fi
done

if [ "${fail}" -ne 0 ]; then
  echo "Refusing to release ${release_commit}: required checks are not green" >&2
  exit 1
fi

echo "Required workflows are green for ${release_commit}."
