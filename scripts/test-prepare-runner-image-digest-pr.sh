#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
prepare_pr_script="${script_dir}/prepare-runner-image-digest-pr.sh"
prepare_pin_script="${script_dir}/prepare-runner-image-pin.sh"
source_module="${repo_root}/modules/nixos/services/github-runner/default.nix"
source_intent="${repo_root}/.github/release-intent.yaml"

for req in "$prepare_pr_script" "$prepare_pin_script" "$source_module" "$source_intent"; do
  if [ ! -f "$req" ]; then
    printf 'FAIL: missing required file: %s\n' "$req" >&2
    exit 1
  fi
done

for tool in git jq perl awk; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'FAIL: required tool not found on PATH: %s\n' "$tool" >&2
    exit 1
  fi
done

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

stub_bin="${tmp_root}/stub-bin"
mkdir -p "$stub_bin"
cat > "${stub_bin}/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 10 ] \
  || [ "$1" != "api" ] \
  || [ "$2" != "-X" ] \
  || [ "$3" != "GET" ] \
  || [ "$4" != "repos/peasant-labs/infra/pulls" ] \
  || [ "$5" != "-f" ] \
  || [ "$6" != "head=peasant-labs:automation/runner-image-digest" ] \
  || [ "$7" != "-f" ] \
  || [ "$8" != "state=open" ] \
  || [ "$9" != "-F" ] \
  || [ "${10}" != "per_page=100" ]; then
  printf 'STUB_GH_ERROR: unexpected gh invocation:' >&2
  printf ' %q' "$@" >&2
  printf '\n' >&2
  exit 99
fi

cat "${GH_STUB_RESPONSE_FILE}"
EOF
chmod +x "${stub_bin}/gh"

current_digest="$(sed -nE 's/^[[:space:]]*default[[:space:]]*=[[:space:]]*"quay\.io\/peasant-labs\/github-runner@(sha256:[0-9a-f]{64})";[[:space:]]*$/\1/p' "$source_module")"
if [ -z "$current_digest" ]; then
  printf 'FAIL: could not extract current digest from %s\n' "$source_module" >&2
  exit 1
fi

digest_old="sha256:1111111111111111111111111111111111111111111111111111111111111111"
digest_new="sha256:2222222222222222222222222222222222222222222222222222222222222222"
digest_alt="sha256:3333333333333333333333333333333333333333333333333333333333333333"
for d_var in digest_old digest_new digest_alt; do
  if [ "${!d_var}" = "$current_digest" ]; then
    printf -v "$d_var" '%s' "sha256:4444444444444444444444444444444444444444444444444444444444444444"
  fi
done

setup_case() {
  local name="$1"
  local author_override="${2:-}"
  local case_dir="${tmp_root}/${name}"
  local remote_git="${case_dir}/remote.git"
  local seed_dir="${case_dir}/seed"
  local work_dir="${case_dir}/work"
  local app_dir="${case_dir}/app"
  local runner_temp="${case_dir}/runner-temp"

  mkdir -p "${seed_dir}/.github" "${seed_dir}/modules/nixos/services/github-runner" "${seed_dir}/scripts" "${runner_temp}"
  cp "$source_intent" "${seed_dir}/.github/release-intent.yaml"
  if [ -n "$author_override" ]; then
    perl -i -pe "s|^automation_author:.*|automation_author: ${author_override}|" "${seed_dir}/.github/release-intent.yaml"
  fi
  cp "$source_module" "${seed_dir}/modules/nixos/services/github-runner/default.nix"
  cp "$prepare_pin_script" "${seed_dir}/scripts/prepare-runner-image-pin.sh"
  cp "$prepare_pr_script" "${seed_dir}/scripts/prepare-runner-image-digest-pr.sh"

  git init --bare -q "$remote_git"
  git -C "$seed_dir" init -q
  git -C "$seed_dir" checkout -q -b main
  git -C "$seed_dir" config user.name test-seed
  git -C "$seed_dir" config user.email test@example.invalid
  git -C "$seed_dir" add .
  git -C "$seed_dir" commit -q -m "chore: seed repo"
  git -C "$seed_dir" remote add origin "$remote_git"
  git -C "$seed_dir" push -q origin main
  git -C "$remote_git" symbolic-ref HEAD refs/heads/main

  git clone -q "$remote_git" "$work_dir"
  git clone -q "$remote_git" "$app_dir"
  printf '%s' "$case_dir"
}

create_remote_bot_branch() {
  local case_dir="$1"
  local digest="$2"
  local tag="${3:-bot-seed}"
  local bot_dir="${case_dir}/${tag}"
  rm -rf "$bot_dir"
  git clone -q "${case_dir}/remote.git" "$bot_dir"
  git -C "$bot_dir" checkout -q -B automation/runner-image-digest origin/main
  bash "${bot_dir}/scripts/prepare-runner-image-pin.sh" "$digest" >/dev/null
  git -C "$bot_dir" config user.name peasant-release-bot
  git -C "$bot_dir" config user.email bots@peasant-labs.invalid
  git -C "$bot_dir" add -- modules/nixos/services/github-runner/default.nix
  git -C "$bot_dir" commit -q -m "chore(runner-image): seed bot branch"
  git -C "$bot_dir" push -q --force origin refs/heads/automation/runner-image-digest:refs/heads/automation/runner-image-digest
  git -C "${case_dir}/remote.git" rev-parse refs/heads/automation/runner-image-digest
}

write_pr_json() {
  local out_file="$1"
  local pr_number="$2"
  local head_sha="$3"
  local user_login="$4"
  local user_type="$5"
  local head_repo="${6:-peasant-labs/infra}"
  local head_ref="${7:-automation/runner-image-digest}"
  local base_repo="${8:-peasant-labs/infra}"
  local base_ref="${9:-main}"
  local state="${10:-open}"

  jq -n \
    --argjson number "$pr_number" \
    --arg state "$state" \
    --arg base_ref "$base_ref" \
    --arg base_repo "$base_repo" \
    --arg head_ref "$head_ref" \
    --arg head_sha "$head_sha" \
    --arg head_repo "$head_repo" \
    --arg user_login "$user_login" \
    --arg user_type "$user_type" \
    '[{
      number: $number,
      state: $state,
      base: {ref: $base_ref, repo: {full_name: $base_repo}},
      head: {ref: $head_ref, sha: $head_sha, repo: (if $head_repo == "null" then null else {full_name: $head_repo} end)},
      user: {login: $user_login, type: $user_type}
    }]' > "$out_file"
}

run_prepare() {
  local work_dir="$1"
  local runner_temp="$2"
  local github_output="$3"
  local digest="$4"
  local main_sha="$5"
  local gh_response_file="$6"

  (
    cd "$work_dir"
    env \
      PATH="${stub_bin}:${PATH}" \
      GH_STUB_RESPONSE_FILE="$gh_response_file" \
      GITHUB_REPOSITORY="peasant-labs/infra" \
      DEFAULT_BRANCH="main" \
      GITHUB_REF="refs/heads/main" \
      GITHUB_SHA="$main_sha" \
      GITHUB_OUTPUT="$github_output" \
      GITHUB_RUN_ID="1001" \
      GITHUB_RUN_ATTEMPT="1" \
      RUNNER_TEMP="$runner_temp" \
      DIGEST="$digest" \
      GH_TOKEN="stub-token" \
      bash scripts/prepare-runner-image-digest-pr.sh
  )
}

get_output() {
  local output_file="$1"
  local key="$2"
  awk -F= -v k="$key" '$1 == k { print substr($0, index($0, "=") + 1); exit }' "$output_file"
}

# 1. Positive update + real bundle verify/fetch + real lease push + 2. Idempotence
case_dir="$(setup_case positive-and-idempotent)"
main_sha="$(git -C "${case_dir}/remote.git" rev-parse refs/heads/main)"
old_head="$(create_remote_bot_branch "$case_dir" "$digest_old" bot-old)"
pr_json="${case_dir}/pr.json"
write_pr_json "$pr_json" 42 "$old_head" "peasant-labs-releaser[bot]" "Bot"

gh_out="${case_dir}/github-output-1.txt"
: > "$gh_out"
run_prepare "${case_dir}/work" "${case_dir}/runner-temp" "$gh_out" "$digest_new" "$main_sha" "$pr_json" >/dev/null

test "$(get_output "$gh_out" push)" = "true"
test "$(get_output "$gh_out" digest)" = "$digest_new"
test "$(get_output "$gh_out" base_sha)" = "$main_sha"
test "$(get_output "$gh_out" expected_head)" = "$old_head"
test "$(get_output "$gh_out" pr_number)" = "42"
prepared_oid="$(get_output "$gh_out" commit_sha)"
if [[ ! "$prepared_oid" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'FAIL: invalid prepared commit_sha: %s\n' "$prepared_oid" >&2
  exit 1
fi
grep -Fq "body<<GH_EOF_1001_1" "$gh_out"

bundle_path="${case_dir}/runner-temp/runner-image-bundle/runner-image-digest.bundle"
manifest_path="${case_dir}/runner-temp/runner-image-bundle/manifest.json"
test -f "$bundle_path"
test -f "$manifest_path"
test "$(jq -r '.commit_sha' "$manifest_path")" = "$prepared_oid"
test "$(jq -r '.digest' "$manifest_path")" = "$digest_new"
test "$(jq -r '.expected_head' "$manifest_path")" = "$old_head"
test "$(jq -r '.pr_number' "$manifest_path")" = "42"

# Simulate update-pr job: verify bundle, fetch into fresh clone, compare OID/digest, push with exact lease.
git -C "${case_dir}/app" bundle verify "$bundle_path" >/dev/null
git -C "${case_dir}/app" fetch -q --no-tags "$bundle_path" refs/heads/automation/runner-image-digest:refs/heads/automation/runner-image-digest
fetched_oid="$(git -C "${case_dir}/app" rev-parse refs/heads/automation/runner-image-digest)"
test "$fetched_oid" = "$prepared_oid"
git -C "${case_dir}/app" show "${fetched_oid}:modules/nixos/services/github-runner/default.nix" | grep -Fq "quay.io/peasant-labs/github-runner@${digest_new}"
git -C "${case_dir}/app" push -q --force-with-lease="refs/heads/automation/runner-image-digest:${old_head}" origin refs/heads/automation/runner-image-digest:refs/heads/automation/runner-image-digest
test "$(git -C "${case_dir}/remote.git" rev-parse refs/heads/automation/runner-image-digest)" = "$prepared_oid"
printf 'PASS: positive-update\n'

# 2. Idempotence: second run with same digest and updated PR head must be push=false with no bundle.
git clone -q "${case_dir}/remote.git" "${case_dir}/work2"
mkdir -p "${case_dir}/runner-temp-2"
write_pr_json "$pr_json" 42 "$prepared_oid" "peasant-labs-releaser[bot]" "Bot"
gh_out2="${case_dir}/github-output-2.txt"
: > "$gh_out2"
run_prepare "${case_dir}/work2" "${case_dir}/runner-temp-2" "$gh_out2" "$digest_new" "$main_sha" "$pr_json" >/dev/null
test "$(get_output "$gh_out2" push)" = "false"
test "$(get_output "$gh_out2" digest)" = "$digest_new"
test "$(get_output "$gh_out2" commit_sha)" = "$prepared_oid"
test "$(get_output "$gh_out2" expected_head)" = "$prepared_oid"
test "$(get_output "$gh_out2" pr_number)" = "42"
test ! -e "${case_dir}/runner-temp-2/runner-image-bundle"
printf 'PASS: idempotence\n'

# 3. Stale lease refusal: concurrent push moves remote head after prepare; real --force-with-lease push fails.
case_dir="$(setup_case stale-lease-refusal)"
main_sha="$(git -C "${case_dir}/remote.git" rev-parse refs/heads/main)"
old_head="$(create_remote_bot_branch "$case_dir" "$digest_old" bot-old)"
pr_json="${case_dir}/pr.json"
write_pr_json "$pr_json" 42 "$old_head" "peasant-labs-releaser[bot]" "Bot"
gh_out="${case_dir}/github-output.txt"
: > "$gh_out"
run_prepare "${case_dir}/work" "${case_dir}/runner-temp" "$gh_out" "$digest_new" "$main_sha" "$pr_json" >/dev/null
expected_head="$(get_output "$gh_out" expected_head)"
test "$expected_head" = "$old_head"

concurrent_head="$(create_remote_bot_branch "$case_dir" "$digest_alt" bot-concurrent)"
test "$concurrent_head" != "$old_head"

bundle_path="${case_dir}/runner-temp/runner-image-bundle/runner-image-digest.bundle"
git -C "${case_dir}/app" bundle verify "$bundle_path" >/dev/null
git -C "${case_dir}/app" fetch -q --no-tags "$bundle_path" refs/heads/automation/runner-image-digest:refs/heads/automation/runner-image-digest
if git -C "${case_dir}/app" push -q --force-with-lease="refs/heads/automation/runner-image-digest:${expected_head}" origin refs/heads/automation/runner-image-digest:refs/heads/automation/runner-image-digest 2>/dev/null; then
  printf 'FAIL: stale --force-with-lease push unexpectedly succeeded\n' >&2
  exit 1
fi
test "$(git -C "${case_dir}/remote.git" rev-parse refs/heads/automation/runner-image-digest)" = "$concurrent_head"
printf 'PASS: stale-lease-refusal\n'

# 4. Absent branch: prepares bundle with blank expected_head so update-pr App job does not run.
case_dir="$(setup_case absent-branch)"
main_sha="$(git -C "${case_dir}/remote.git" rev-parse refs/heads/main)"
pr_json="${case_dir}/pr.json"
printf '[]\n' > "$pr_json"
gh_out="${case_dir}/github-output.txt"
: > "$gh_out"
run_prepare "${case_dir}/work" "${case_dir}/runner-temp" "$gh_out" "$digest_new" "$main_sha" "$pr_json" >/dev/null
test "$(get_output "$gh_out" push)" = "true"
test -z "$(get_output "$gh_out" expected_head)"
test -z "$(get_output "$gh_out" pr_number)"
bundle_path="${case_dir}/runner-temp/runner-image-bundle/runner-image-digest.bundle"
git -C "${case_dir}/app" bundle verify "$bundle_path" >/dev/null
printf 'PASS: absent-branch-no-app-write\n'

# 5. Wrong author refusals (human, wrong bot, missing [bot] suffix, User type on [bot] login).
expect_prepare_failure() {
  local name="$1"
  local pr_login="$2"
  local pr_type="$3"
  local head_repo="${4:-peasant-labs/infra}"
  local base_ref="${5:-main}"
  local author_override="${6:-}"

  local case_dir
  case_dir="$(setup_case "$name" "$author_override")"
  local main_sha
  main_sha="$(git -C "${case_dir}/remote.git" rev-parse refs/heads/main)"
  local old_head
  old_head="$(create_remote_bot_branch "$case_dir" "$digest_old" bot-old)"
  local pr_json="${case_dir}/pr.json"
  write_pr_json "$pr_json" 42 "$old_head" "$pr_login" "$pr_type" "$head_repo" "automation/runner-image-digest" "peasant-labs/infra" "$base_ref" "open"
  local gh_out="${case_dir}/github-output.txt"
  : > "$gh_out"

  if run_prepare "${case_dir}/work" "${case_dir}/runner-temp" "$gh_out" "$digest_new" "$main_sha" "$pr_json" >/dev/null 2>&1; then
    printf 'FAIL: %s unexpectedly succeeded\n' "$name" >&2
    exit 1
  fi
  test ! -s "$gh_out"
  test ! -e "${case_dir}/runner-temp/runner-image-bundle"
  printf 'PASS: %s\n' "$name"
}

expect_prepare_failure "wrong-author-human-refusal" "alice" "User"
expect_prepare_failure "wrong-author-other-bot-refusal" "other-bot[bot]" "Bot"
expect_prepare_failure "wrong-author-missing-bot-suffix-refusal" "peasant-labs-releaser" "Bot"
expect_prepare_failure "wrong-author-nonbot-type-refusal" "peasant-labs-releaser[bot]" "User"
expect_prepare_failure "invalid-automation-author-config-refusal" "peasant-labs-releaser[bot]" "Bot" "peasant-labs/infra" "main" "peasant-labs-releaser[bot]"
expect_prepare_failure "cross-repo-fork-refusal" "peasant-labs-releaser[bot]" "Bot" "attacker/infra"
expect_prepare_failure "wrong-base-ref-refusal" "peasant-labs-releaser[bot]" "Bot" "peasant-labs/infra" "other-branch"
