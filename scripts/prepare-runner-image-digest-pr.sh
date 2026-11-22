#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

canonical_repo="peasant-labs/infra"
canonical_owner="${canonical_repo%%/*}"
canonical_branch="main"
canonical_signer="https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main"
bot_branch="automation/runner-image-digest"
target_path="modules/nixos/services/github-runner/default.nix"

test "${GITHUB_REPOSITORY:-}" = "${canonical_repo}" || {
  echo "::error::unexpected repository ${GITHUB_REPOSITORY:-}; expected ${canonical_repo}"
  exit 1
}
test "${DEFAULT_BRANCH:-}" = "${canonical_branch}" || {
  echo "::error::unexpected default branch ${DEFAULT_BRANCH:-}; expected ${canonical_branch}"
  exit 1
}
test "${GITHUB_REF:-}" = "refs/heads/${canonical_branch}" || {
  echo "::error::unexpected ref ${GITHUB_REF:-}; expected refs/heads/${canonical_branch}"
  exit 1
}
if [[ ! "${DIGEST:-}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "::error::missing or malformed verified digest output: ${DIGEST:-}"
  exit 1
fi
test -n "${GITHUB_SHA:-}" || {
  echo "::error::missing GITHUB_SHA"
  exit 1
}
test -n "${GITHUB_OUTPUT:-}" || {
  echo "::error::missing GITHUB_OUTPUT"
  exit 1
}
test -n "${RUNNER_TEMP:-}" || {
  echo "::error::missing RUNNER_TEMP"
  exit 1
}
test "$(git rev-parse HEAD)" = "${GITHUB_SHA}" || {
  echo "::error::checked out HEAD does not match GITHUB_SHA"
  exit 1
}

automation_author="$(sed -nE 's/^automation_author:[[:space:]]*([^[:space:]#]+).*$/\1/p' .github/release-intent.yaml)"
test -n "${automation_author}" || {
  echo "::error::could not read automation_author from .github/release-intent.yaml"
  exit 1
}
if [[ ! "${automation_author}" =~ ^app/([a-z0-9]([a-z0-9-]*[a-z0-9])?)$ ]]; then
  echo "::error::invalid automation_author ${automation_author} in .github/release-intent.yaml (expected app/<slug>)"
  exit 1
fi
expected_pr_author="${BASH_REMATCH[1]}[bot]"

configured_bot_branch="$(sed -nE 's/^[[:space:]]*runner-image:[[:space:]]*([^[:space:]#]+).*$/\1/p' .github/release-intent.yaml)"
test "${configured_bot_branch}" = "${bot_branch}" || {
  echo "::error::runner-image bot branch in .github/release-intent.yaml (${configured_bot_branch}) does not match ${bot_branch}"
  exit 1
}

git fetch --no-tags origin "refs/heads/${canonical_branch}:refs/remotes/origin/${canonical_branch}"
base_sha="$(git rev-parse "refs/remotes/origin/${canonical_branch}")"
git merge-base --is-ancestor "${GITHUB_SHA}" "${base_sha}" || {
  echo "::error::GITHUB_SHA is not an ancestor of origin/${canonical_branch}"
  exit 1
}

prs_json="$(gh api -X GET "repos/${GITHUB_REPOSITORY}/pulls" -f "head=${canonical_owner}:${bot_branch}" -f state=open -F per_page=100)"
printf '%s' "${prs_json}" | jq -e 'type == "array"' >/dev/null || {
  echo "::error::unexpected response from GitHub pulls REST API"
  exit 1
}
pr_count="$(printf '%s' "${prs_json}" | jq 'length')"
if [ "${pr_count}" -gt 1 ]; then
  echo "::error::found ${pr_count} open PRs for ${bot_branch}; refusing to risk duplicate PRs"
  exit 1
fi

pr_number=""
pr_head_oid=""
if [ "${pr_count}" -eq 1 ]; then
  pr_number="$(printf '%s' "${prs_json}" | jq -r '.[0].number // empty')"
  pr_state="$(printf '%s' "${prs_json}" | jq -r '.[0].state // empty')"
  pr_base_repo="$(printf '%s' "${prs_json}" | jq -r '.[0].base.repo.full_name // empty')"
  pr_base_ref="$(printf '%s' "${prs_json}" | jq -r '.[0].base.ref // empty')"
  pr_head_repo="$(printf '%s' "${prs_json}" | jq -r '.[0].head.repo.full_name // empty')"
  pr_head_ref="$(printf '%s' "${prs_json}" | jq -r '.[0].head.ref // empty')"
  pr_head_oid="$(printf '%s' "${prs_json}" | jq -r '.[0].head.sha // empty')"
  pr_author="$(printf '%s' "${prs_json}" | jq -r '.[0].user.login // empty')"
  pr_author_type="$(printf '%s' "${prs_json}" | jq -r '.[0].user.type // empty')"

  if [[ ! "${pr_number}" =~ ^[0-9]+$ ]]; then
    echo "::error::invalid PR number in REST response: ${pr_number}"
    exit 1
  fi
  test "${pr_state}" = "open" || {
    echo "::error::PR #${pr_number} state is ${pr_state}, expected open"
    exit 1
  }
  test "${pr_head_repo}" = "${canonical_repo}" || {
    echo "::error::open PR #${pr_number} head repo is ${pr_head_repo}, expected ${canonical_repo}"
    exit 1
  }
  test "${pr_head_ref}" = "${bot_branch}" || {
    echo "::error::open PR #${pr_number} head ref is ${pr_head_ref}, expected ${bot_branch}"
    exit 1
  }
  test "${pr_base_repo}" = "${canonical_repo}" || {
    echo "::error::open PR #${pr_number} base repo is ${pr_base_repo}, expected ${canonical_repo}"
    exit 1
  }
  test "${pr_base_ref}" = "${canonical_branch}" || {
    echo "::error::open PR #${pr_number} targets ${pr_base_ref} instead of ${canonical_branch}"
    exit 1
  }
  if [[ ! "${pr_head_oid}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "::error::open PR #${pr_number} has invalid head sha: ${pr_head_oid}"
    exit 1
  fi
  test "${pr_author_type}" = "Bot" || {
    echo "::error::open PR #${pr_number} author type is ${pr_author_type}, expected Bot"
    exit 1
  }
  test "${pr_author}" = "${expected_pr_author}" || {
    echo "::error::open PR #${pr_number} is authored by ${pr_author}, not ${expected_pr_author}"
    exit 1
  }
fi

expected_head=""
remote_ref_line="$(git ls-remote --heads origin "refs/heads/${bot_branch}")"
if [ -n "${remote_ref_line}" ]; then
  line_count="$(printf '%s\n' "${remote_ref_line}" | wc -l)"
  test "${line_count}" -eq 1 || {
    echo "::error::ambiguous remote refs for ${bot_branch}"
    exit 1
  }
  expected_head="${remote_ref_line%%$'\t'*}"
  if [[ ! "${expected_head}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "::error::invalid remote head OID for ${bot_branch}: ${expected_head}"
    exit 1
  fi
  git fetch --no-tags origin "refs/heads/${bot_branch}:refs/remotes/origin/${bot_branch}"
  test "$(git rev-parse "refs/remotes/origin/${bot_branch}")" = "${expected_head}" || {
    echo "::error::fetched ${bot_branch} does not match ls-remote head ${expected_head}"
    exit 1
  }
  if [ -n "${pr_head_oid}" ] && [ "${pr_head_oid}" != "${expected_head}" ]; then
    echo "::error::open PR #${pr_number} head ${pr_head_oid} does not match remote branch head ${expected_head}"
    exit 1
  fi
  merge_base="$(git merge-base "${base_sha}" "refs/remotes/origin/${bot_branch}")"
  test -n "${merge_base}" || {
    echo "::error::remote ${bot_branch} shares no history with ${canonical_branch}; refusing to overwrite unrelated branch"
    exit 1
  }
  remote_commits="$(git rev-list --count "${merge_base}..refs/remotes/origin/${bot_branch}")"
  test "${remote_commits}" -eq 1 || {
    echo "::error::remote ${bot_branch} has ${remote_commits} commits ahead of merge-base; refusing to overwrite unrelated branch"
    exit 1
  }
  remote_files="$(git diff --name-only "${merge_base}" "refs/remotes/origin/${bot_branch}")"
  test "${remote_files}" = "${target_path}" || {
    echo "::error::remote ${bot_branch} modifies unexpected paths (${remote_files}); refusing to overwrite unrelated branch"
    exit 1
  }
  remote_numstat="$(git diff --numstat "${merge_base}" "refs/remotes/origin/${bot_branch}")"
  test "${remote_numstat}" = $'1\t1\t'"${target_path}" || {
    echo "::error::remote ${bot_branch} is not a one-line pin change; refusing to overwrite"
    exit 1
  }
elif [ -n "${pr_number}" ]; then
  echo "::error::open PR #${pr_number} exists without remote branch ${bot_branch}"
  exit 1
fi

git checkout -q -B "${bot_branch}" "${base_sha}"
bash "${script_dir}/prepare-runner-image-pin.sh" "${DIGEST}"

test -z "$(git status --porcelain --untracked-files=all | grep -v -F " M ${target_path}" || true)" || {
  echo "::error::unexpected working tree changes after prepare-runner-image-pin.sh"
  git status --short
  exit 1
}

write_outputs() {
  local push_val="$1"
  local commit_val="$2"
  local title_val="$3"
  local body_val="$4"
  local delim="GH_EOF_${GITHUB_RUN_ID:-local}_${GITHUB_RUN_ATTEMPT:-1}"
  {
    echo "push=${push_val}"
    echo "digest=${DIGEST}"
    echo "base_sha=${base_sha}"
    echo "commit_sha=${commit_val}"
    echo "expected_head=${expected_head}"
    echo "pr_number=${pr_number}"
    echo "title=${title_val}"
    if [ -n "${body_val}" ]; then
      echo "body<<${delim}"
      printf '%s\n' "${body_val}"
      echo "${delim}"
    else
      echo "body="
    fi
  } >> "${GITHUB_OUTPUT}"
}

if git diff --quiet -- "${target_path}"; then
  echo "Default module pin already matches ${DIGEST}; nothing to prepare."
  write_outputs "false" "" "" ""
  exit 0
fi

test "$(git status --porcelain)" = " M ${target_path}" || {
  echo "::error::expected only ${target_path} to be modified"
  exit 1
}
test "$(git diff --numstat -- "${target_path}")" = $'1\t1\t'"${target_path}" || {
  echo "::error::expected exact one-line diff on ${target_path}"
  git diff -- "${target_path}"
  exit 1
}
added_line="$(git diff -U0 -- "${target_path}" | grep -E '^\+[^+]' || true)"
if [[ "${added_line}" != *"quay.io/peasant-labs/github-runner@${DIGEST}"* ]]; then
  echo "::error::diff does not bind to verified digest ${DIGEST}"
  exit 1
fi
grep -Fq "\"${canonical_signer}\"" "${target_path}" || {
  echo "::error::canonical imageSigner default missing from ${target_path}"
  exit 1
}

short_digest="${DIGEST#sha256:}"
short_digest="${short_digest:0:12}"
title="chore(runner-image): pin runner image digest ${short_digest}"
body="$(printf "Updates the default runner container image digest in \`%s\` to \`%s\`.\n\n- Verified signer: \`%s\`\n- Base commit: \`%s\`\n\nAuto-merge is intentionally not enabled; this PR requires review and CI." \
  "${target_path}" "${DIGEST}" "${canonical_signer}" "${base_sha}")"

git config user.name peasant-release-bot
git config user.email bots@peasant-labs.invalid
git add -- "${target_path}"
git commit -m "${title}"
commit_sha="$(git rev-parse "refs/heads/${bot_branch}")"

test "$(git rev-list --count "${base_sha}..refs/heads/${bot_branch}")" = "1" || {
  echo "::error::expected exactly 1 commit on ${bot_branch} above ${base_sha}"
  exit 1
}
test "$(git diff --name-only "${base_sha}" "refs/heads/${bot_branch}")" = "${target_path}" || {
  echo "::error::commit touches unexpected files"
  exit 1
}
test "$(git diff --numstat "${base_sha}" "refs/heads/${bot_branch}")" = $'1\t1\t'"${target_path}" || {
  echo "::error::commit is not an exact one-line diff"
  exit 1
}

if [ -n "${expected_head}" ] && [ -n "${pr_number}" ] \
   && [ "$(git rev-parse "${expected_head}^")" = "${base_sha}" ] \
   && git diff --quiet "${expected_head}" "refs/heads/${bot_branch}"; then
  echo "Open PR #${pr_number} already carries the identical candidate commit ${expected_head}."
  write_outputs "false" "${expected_head}" "${title}" "${body}"
  exit 0
fi

bundle_dir="${RUNNER_TEMP}/runner-image-bundle"
mkdir -p "${bundle_dir}"
bundle_path="${bundle_dir}/runner-image-digest.bundle"
git bundle create "${bundle_path}" "refs/heads/${bot_branch}" "^${GITHUB_SHA}"
git bundle verify "${bundle_path}"
git bundle list-heads "${bundle_path}" | awk -v sha="${commit_sha}" -v ref="refs/heads/${bot_branch}" '
  NF != 2 || $1 != sha || $2 != ref { exit 1 }
  END { if (NR != 1) exit 1 }
' || {
  echo "::error::bundle heads verification failed for ${commit_sha} refs/heads/${bot_branch}"
  exit 1
}

jq -n \
  --arg repository "${GITHUB_REPOSITORY}" \
  --arg default_branch "${DEFAULT_BRANCH}" \
  --arg base_sha "${base_sha}" \
  --arg branch "${bot_branch}" \
  --arg commit_sha "${commit_sha}" \
  --arg digest "${DIGEST}" \
  --arg signer "${canonical_signer}" \
  --arg expected_head "${expected_head}" \
  --arg pr_number "${pr_number}" \
  --arg title "${title}" \
  '{repository: $repository, default_branch: $default_branch, base_sha: $base_sha, branch: $branch, commit_sha: $commit_sha, digest: $digest, signer: $signer, expected_head: $expected_head, pr_number: $pr_number, title: $title}' \
  > "${bundle_dir}/manifest.json"

write_outputs "true" "${commit_sha}" "${title}" "${body}"
