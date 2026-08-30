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

# Workflows that must have a successful run for this exact commit, keyed by the
# workflow file path rather than the display name: a name is free text, so a
# second workflow could otherwise be added that calls itself "Go", succeeds
# trivially, and satisfies this check while the real one fails.
#
# Test Release Pipeline is deliberately absent: it is path-filtered, so a
# release commit that touches no packaging file legitimately has no run.
required_workflow_paths=(
  ".github/workflows/go.yml"
  ".github/workflows/go-cross.yml"
  ".github/workflows/vulncheck.yml"
)

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
  # exclude_pull_requests: a pull_request run checks out GitHub's synthetic
  # merge ref, so a green run proves the merge result was good, not that this
  # commit was ever built on its own.
  if ! runs_json="$(
    gh api --paginate \
      "repos/${repository}/actions/runs?head_sha=${release_commit}&exclude_pull_requests=true&per_page=100" \
      --jq '.workflow_runs[] | {path, event, head_branch, head_sha, status, conclusion, run_number, run_attempt}' \
      2>"${error_file}" | jq -s '.'
  )"; then
    cat "${error_file}" >&2
    exit 1
  fi
else
  runs_json="$(cat "${fixture}")"
fi

if ! jq -e 'type == "array" and all(.[]; type == "object" and (.path | type == "string"))' \
  <<<"${runs_json}" >/dev/null 2>&1; then
  echo "Invalid workflow run response for ${release_commit}" >&2
  exit 1
fi

fail=0
for workflow in "${required_workflow_paths[@]}"; do
  # Only runs that actually built this commit count: the right workflow file,
  # triggered by a push to main or a manual dispatch, reporting this SHA. Take
  # the newest such run; an older success must not paper over a later failure.
  latest="$(jq -c --arg path "${workflow}" --arg sha "${release_commit}" '
    [ .[]
      | select(.path == $path)
      | select(.head_sha == $sha)
      | select(.event == "push" or .event == "workflow_dispatch")
      | select(.event != "push" or .head_branch == "main")
    ]
    | sort_by([.run_number // 0, .run_attempt // 0])
    | last // null' <<<"${runs_json}")"

  if [ "${latest}" = "null" ]; then
    echo "Required workflow ${workflow} has no qualifying run for ${release_commit} (no run)" >&2
    fail=1
    continue
  fi

  status="$(jq -r '.status // "unknown"' <<<"${latest}")"
  conclusion="$(jq -r '.conclusion // "none"' <<<"${latest}")"
  if [ "${status}" != "completed" ] || [ "${conclusion}" != "success" ]; then
    observed="${conclusion}"
    if [ "${status}" != "completed" ]; then
      observed="${status}"
    fi
    echo "Required workflow ${workflow} did not succeed for ${release_commit} (${observed})" >&2
    fail=1
  fi
done

if [ "${fail}" -ne 0 ]; then
  echo "Refusing to release ${release_commit}: required checks are not green" >&2
  exit 1
fi

echo "Required workflows are green for ${release_commit}."
