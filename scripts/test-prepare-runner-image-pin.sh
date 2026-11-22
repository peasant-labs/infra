#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
prepare_script="${script_dir}/prepare-runner-image-pin.sh"
source_module="${repo_root}/modules/nixos/services/github-runner/default.nix"

if [ ! -f "$prepare_script" ]; then
  printf 'FAIL: missing preparation script: %s\n' "$prepare_script" >&2
  exit 1
fi

if [ ! -f "$source_module" ]; then
  printf 'FAIL: missing source module: %s\n' "$source_module" >&2
  exit 1
fi

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

current_digest="$(sed -nE 's/^[[:space:]]*default[[:space:]]*=[[:space:]]*"quay\.io\/peasant-labs\/github-runner@(sha256:[0-9a-f]{64})";[[:space:]]*$/\1/p' "$source_module")"
if [ -z "$current_digest" ]; then
  printf 'FAIL: could not extract current digest from %s\n' "$source_module" >&2
  exit 1
fi

new_digest="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
if [ "$new_digest" = "$current_digest" ]; then
  new_digest="sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
fi

canonical_signer='https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main'

setup_case() {
  local name="$1"
  local case_dir="${tmp_root}/${name}"
  mkdir -p "${case_dir}/modules/nixos/services/github-runner"
  cp "$source_module" "${case_dir}/modules/nixos/services/github-runner/default.nix"
  cp "$source_module" "${case_dir}/baseline.nix"
  printf '%s' "$case_dir"
}

assert_only_expected_files() {
  local case_dir="$1"
  local actual
  actual="$(cd "$case_dir" && find . -type f | sort | tr '\n' ' ')"
  local expected="./baseline.nix ./modules/nixos/services/github-runner/default.nix "
  if [ "$actual" != "$expected" ]; then
    printf 'FAIL: unexpected files in %s: %s\n' "$case_dir" "$actual" >&2
    exit 1
  fi
}

assert_unchanged() {
  local case_dir="$1"
  local target="${case_dir}/modules/nixos/services/github-runner/default.nix"
  local baseline="${case_dir}/baseline.nix"
  if ! cmp -s "$baseline" "$target"; then
    printf 'FAIL: file unexpectedly modified in %s\n' "$case_dir" >&2
    diff -u "$baseline" "$target" >&2 || true
    exit 1
  fi
  assert_only_expected_files "$case_dir"
}

# 1. Current digest is a no-op.
case_dir="$(setup_case current-digest-noop)"
bash "$prepare_script" --root "$case_dir" "$current_digest" >/dev/null
assert_unchanged "$case_dir"
printf 'PASS: current-digest-noop\n'

# 2. New digest changes exactly one line and preserves imageSigner.
case_dir="$(setup_case new-digest-one-line)"
target="${case_dir}/modules/nixos/services/github-runner/default.nix"
baseline="${case_dir}/baseline.nix"
bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null
assert_only_expected_files "$case_dir"

diff_out="$(diff -u "$baseline" "$target" || true)"
removed_count="$(printf '%s\n' "$diff_out" | grep -cE '^-([^-]|$)' || true)"
added_count="$(printf '%s\n' "$diff_out" | grep -cE '^\+([^+]|$)' || true)"
if [ "$removed_count" -ne 1 ] || [ "$added_count" -ne 1 ]; then
  printf 'FAIL: expected 1 removed and 1 added line, got -%s +%s\n' "$removed_count" "$added_count" >&2
  printf '%s\n' "$diff_out" >&2
  exit 1
fi
if ! grep -Fq "quay.io/peasant-labs/github-runner@${new_digest}" "$target"; then
  printf 'FAIL: new digest not found in updated module\n' >&2
  exit 1
fi
if ! grep -Fq "$canonical_signer" "$target"; then
  printf 'FAIL: canonical signer was not preserved\n' >&2
  exit 1
fi
printf 'PASS: new-digest-one-line\n'

# 3. Invalid digests are rejected with no writes.
invalid_digests=(
  ""
  "sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"
  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde"
  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
  "sha512:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  "sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
)
idx=0
for bad_digest in "${invalid_digests[@]}"; do
  idx=$((idx + 1))
  case_dir="$(setup_case "invalid-digest-${idx}")"
  if bash "$prepare_script" --root "$case_dir" "$bad_digest" >/dev/null 2>&1; then
    printf 'FAIL: accepted invalid digest: %q\n' "$bad_digest" >&2
    exit 1
  fi
  assert_unchanged "$case_dir"
done
printf 'PASS: invalid-digests (%d cases)\n' "$idx"

# 4. Missing imageRef anchor is refused with no writes.
case_dir="$(setup_case missing-imageref)"
target="${case_dir}/modules/nixos/services/github-runner/default.nix"
baseline="${case_dir}/baseline.nix"
perl -0777 -i -pe 's/imageRef\s*=\s*mkOption/imageRefRemoved = mkOption/' "$target"
cp "$target" "$baseline"
if bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null 2>&1; then
  printf 'FAIL: accepted missing imageRef anchor\n' >&2
  exit 1
fi
assert_unchanged "$case_dir"
printf 'PASS: missing-imageref\n'

# 5. Missing/altered imageSigner anchor is refused with no writes.
case_dir="$(setup_case missing-signer)"
target="${case_dir}/modules/nixos/services/github-runner/default.nix"
baseline="${case_dir}/baseline.nix"
perl -0777 -i -pe 's/\@refs\/heads\/main/\@refs\/heads\/other/' "$target"
cp "$target" "$baseline"
if bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null 2>&1; then
  printf 'FAIL: accepted altered imageSigner anchor\n' >&2
  exit 1
fi
assert_unchanged "$case_dir"
printf 'PASS: missing-signer\n'

# 6. Ambiguous imageRef anchor is refused with no writes.
case_dir="$(setup_case ambiguous-imageref)"
target="${case_dir}/modules/nixos/services/github-runner/default.nix"
baseline="${case_dir}/baseline.nix"
perl -0777 -i -pe 's/(default\s*=\s*"quay\.io\/peasant-labs\/github-runner\@sha256:[0-9a-f]{64}";)/$1\n      $1/' "$target"
cp "$target" "$baseline"
if bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null 2>&1; then
  printf 'FAIL: accepted ambiguous imageRef anchor\n' >&2
  exit 1
fi
assert_unchanged "$case_dir"
printf 'PASS: ambiguous-imageref\n'

# 7. Ambiguous imageSigner anchor is refused with no writes.
case_dir="$(setup_case ambiguous-signer)"
target="${case_dir}/modules/nixos/services/github-runner/default.nix"
baseline="${case_dir}/baseline.nix"
perl -0777 -i -pe 's/(default\s*=\s*"https:\/\/github\.com\/peasant-labs\/infra\/\.github\/workflows\/runner-image\.yml\@refs\/heads\/main";)/$1\n      $1/' "$target"
cp "$target" "$baseline"
if bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null 2>&1; then
  printf 'FAIL: accepted ambiguous imageSigner anchor\n' >&2
  exit 1
fi
assert_unchanged "$case_dir"
printf 'PASS: ambiguous-signer\n'

# 8. Missing target file is refused with no writes.
case_dir="${tmp_root}/missing-target"
mkdir -p "$case_dir"
if bash "$prepare_script" --root "$case_dir" "$new_digest" >/dev/null 2>&1; then
  printf 'FAIL: accepted missing target file\n' >&2
  exit 1
fi
if [ -n "$(find "$case_dir" -mindepth 1)" ]; then
  printf 'FAIL: unexpected files created in %s\n' "$case_dir" >&2
  exit 1
fi
printf 'PASS: missing-target\n'
