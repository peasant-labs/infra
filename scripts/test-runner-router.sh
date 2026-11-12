#!/usr/bin/env bash
#
# Test the real runner-router.yml routing logic.
#
# The router is inline shell in .github/workflows/runner-router.yml, so this
# test extracts the `Pick a runner` step's `run:` block from that file and
# executes it against a stub `gh` and fixture health records. It never copies
# the script: if the workflow drifts, extraction or the assertions fail.
#
# Runtime is bash + jq + coreutils only (no yq, no Python). Text substitution
# for the mutation round uses perl, which the pool image carries the same way
# scripts/check-production-guards.sh already relies on it.
#
# Fixtures: scripts/testdata/runner-router.json

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/runner-router.yml"
FIXTURE="$ROOT/scripts/testdata/runner-router.json"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

# Extract the `run: |` block of the step with `id: route` from the real
# workflow file, de-indented. Fails if the shape drifts.
extract_route_script() {
  awk '
    /^[[:space:]]*id:[[:space:]]+route([[:space:]]|$)/ { seen = 1; next }
    seen && !started && $0 ~ /run:[[:space:]]*\|[[:space:]]*$/ {
      started = 1
      strip = index($0, "run:") + 1
      next
    }
    started {
      if ($0 ~ /^[[:space:]]*$/) { print ""; next }
      if (substr($0, 1, strip) ~ /^ +$/) { print substr($0, strip + 1); next }
      exit
    }
  ' "$WORKFLOW"
}

# Check the workflow declares required_slots as a number defaulting to 1.
check_workflow_decl() {
  local file="$1"
  local block="$TMP/decl.txt"
  grep -q '^[[:space:]]*required_slots:[[:space:]]*$' "$file" || return 1
  awk '
    /^[[:space:]]*required_slots:[[:space:]]*$/ { inblock = 1; next }
    inblock && /^ {0,6}[A-Za-z_]+:/ { inblock = 0 }
    inblock { print }
  ' "$file" > "$block"
  grep -Eq '^[[:space:]]*type:[[:space:]]*number[[:space:]]*$' "$block" || return 1
  grep -Eq '^[[:space:]]*default:[[:space:]]*1[[:space:]]*$' "$block" || return 1
  return 0
}

# Read one GitHub output value with last-write-wins semantics (the `runner`
# output uses a heredoc delimiter; plain `key=value` lines take the last).
github_output_get() {
  local file="$1" key="$2"
  awk -v key="$key" '
    BEGIN { heredoc = key "<<RUNNER_ROUTER_EOF"; prefix = key "=" }
    $0 == heredoc { capture = 1; val = ""; next }
    capture {
      if ($0 == "RUNNER_ROUTER_EOF") { capture = 0; last = val }
      else val = (val == "" ? $0 : val "\n" $0)
      next
    }
    index($0, prefix) == 1 { last = substr($0, length(prefix) + 1) }
    END { print last }
  ' "$file"
}

# Run one fixture case against a script file. Prints the failure, if any.
run_case() {
  local idx="$1" script="$2" workdir="$3" quiet="${4:-0}"
  local name required labels token gh_exit want_runner_ref want_pool want_reason
  name="$(jq -r ".cases[$idx].name" "$FIXTURE")"
  required="$(jq -r ".cases[$idx].required_slots" "$FIXTURE")"
  labels="$(jq -r ".cases[$idx].labels" "$FIXTURE")"
  token="$(jq -r ".cases[$idx].token" "$FIXTURE")"
  gh_exit="$(jq -r ".cases[$idx].gh_exit" "$FIXTURE")"
  want_runner_ref="$(jq -r ".cases[$idx].expect.runner" "$FIXTURE")"
  want_pool="$(jq -r ".cases[$idx].expect.pool" "$FIXTURE")"
  want_reason="$(jq -r ".cases[$idx].expect.reason" "$FIXTURE")"
  local fallback
  fallback="$(jq -r ".fallback" "$FIXTURE")"

  local stubdir="$workdir/stub-$idx"
  mkdir -p "$stubdir"
  cat > "$stubdir/gh" <<'STUB_EOF'
#!/usr/bin/env bash
if [ "${GH_STUB_FAIL:-0}" = "1" ]; then
  echo "mock gh: variable not found" >&2
  exit 1
fi
printf '%s' "${GH_STUB_RECORD:-}"
STUB_EOF
  chmod +x "$stubdir/gh"

  local record_json=""
  if [ "$(jq -r ".cases[$idx].record | type" "$FIXTURE")" = "object" ]; then
    local now age
    now="$(date -u +%s)"
    age="$(jq -r ".cases[$idx].record.age_seconds" "$FIXTURE")"
    record_json="$(jq -c --argjson now "$now" --argjson age "$age" \
      '.record | del(.age_seconds)
        | .timestamp = (($now - $age) | gmtime | strftime("%Y-%m-%dT%H:%M:%SZ"))' \
      < <(jq -c ".cases[$idx]" "$FIXTURE"))"
  fi

  local ghout="$workdir/output-$idx"
  : > "$ghout"
  local logout="$workdir/log-$idx"
  local stub_fail="0"
  [ "$gh_exit" = "1" ] && stub_fail="1"
  local rc=0
  GH_STUB_FAIL="$stub_fail" GH_STUB_RECORD="$record_json" \
    GITHUB_OUTPUT="$ghout" FALLBACK="$fallback" LABELS="$labels" \
    REQUIRED_SLOTS="$required" GH_TOKEN="$token" \
    PATH="$stubdir:$PATH" bash "$script" >"$logout" 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    [ "$quiet" = "0" ] && printf 'FAIL [%s]: router script exited %d\n--- log ---\n%s\n' \
      "$name" "$rc" "$(cat "$logout")"
    return 1
  fi

  local want_runner="$labels"
  [ "$want_runner_ref" = "fallback" ] && want_runner="$fallback"
  local got_runner got_pool got_reason
  got_runner="$(github_output_get "$ghout" runner)"
  got_pool="$(github_output_get "$ghout" pool)"
  got_reason="$(github_output_get "$ghout" reason)"
  local mismatch=""
  [ "$got_runner" = "$want_runner" ] || mismatch="${mismatch} runner: got [${got_runner}] want [${want_runner}]"
  [ "$got_pool" = "$want_pool" ] || mismatch="${mismatch} pool: got [${got_pool}] want [${want_pool}]"
  [ "$got_reason" = "$want_reason" ] || mismatch="${mismatch} reason: got [${got_reason}] want [${want_reason}]"
  local want_notice
  want_notice="$(jq -r ".cases[$idx].expect.notice_contains // empty" "$FIXTURE")"
  if [ -n "$want_notice" ] && ! grep -qF "$want_notice" "$logout"; then
    mismatch="${mismatch} notice: log lacks [${want_notice}]"
  fi
  if [ -n "$mismatch" ]; then
    [ "$quiet" = "0" ] && printf 'FAIL [%s]:%s\n--- log ---\n%s\n' \
      "$name" "$mismatch" "$(cat "$logout")"
    return 1
  fi
  [ "$quiet" = "0" ] && printf 'PASS [%s]\n' "$name"
  return 0
}

printf '== runner-router: extract the real route step ==\n'
extract_route_script > "$TMP/route.sh"
[ -s "$TMP/route.sh" ] || fail "extracted route script is empty"
grep -q 'GITHUB_OUTPUT' "$TMP/route.sh" || fail "extraction missed the output plumbing"
grep -q 'REQUIRED_SLOTS' "$TMP/route.sh" || fail "extraction missed REQUIRED_SLOTS handling"
grep -q 'RUNNER_ROUTER_EOF' "$TMP/route.sh" || fail "extraction missed the runner heredoc"
printf 'PASS [extract]: route step extracted from runner-router.yml\n'

printf '== runner-router: input declaration ==\n'
check_workflow_decl "$WORKFLOW" \
  || fail "required_slots must be declared with type: number and default: 1"
printf 'PASS [decl]: required_slots declared type number default 1\n'

printf '== runner-router: case manifest ==\n'
jq -r '.cases[].name' "$FIXTURE" | sort > "$TMP/cases.actual"
jq -r '.expected_case_names[]' "$FIXTURE" | sort > "$TMP/cases.expected"
if ! diff -u "$TMP/cases.expected" "$TMP/cases.actual" > "$TMP/cases.diff"; then
  cat "$TMP/cases.diff"
  fail "fixture cases do not match the required-name manifest"
fi
jq -r '.mutations[].name' "$FIXTURE" | sort > "$TMP/muts.actual"
jq -r '.expected_mutation_names[]' "$FIXTURE" | sort > "$TMP/muts.expected"
if ! diff -u "$TMP/muts.expected" "$TMP/muts.actual" > "$TMP/muts.diff"; then
  cat "$TMP/muts.diff"
  fail "fixture mutations do not match the required-name manifest"
fi
printf 'PASS [manifest]: every case and mutation is named in its manifest\n'

printf '== runner-router: cases ==\n'
NCASES="$(jq -r '.cases | length' "$FIXTURE")"
i=0
while [ "$i" -lt "$NCASES" ]; do
  run_case "$i" "$TMP/route.sh" "$TMP" || fail "case round failed"
  i=$((i + 1))
done

printf '== runner-router: named mutations (each must break at least one case) ==\n'
NMUTS="$(jq -r '.mutations | length' "$FIXTURE")"
i=0
while [ "$i" -lt "$NMUTS" ]; do
  mname="$(jq -r ".mutations[$i].name" "$FIXTURE")"
  mtarget="$(jq -r ".mutations[$i].target" "$FIXTURE")"
  mold="$(jq -r ".mutations[$i].old" "$FIXTURE")"
  mnew="$(jq -r ".mutations[$i].new" "$FIXTURE")"
  mdir="$TMP/mut-$i"
  mkdir -p "$mdir"
  mscript="$TMP/route.sh"
  mworkflow="$WORKFLOW"
  if [ "$mtarget" = "script" ]; then
    mscript="$mdir/route.sh"
    cp "$TMP/route.sh" "$mscript"
    MUT_OLD="$mold" MUT_NEW="$mnew" perl -0777 -pi -e \
      '$c += s/\Q$ENV{MUT_OLD}\E/$ENV{MUT_NEW}/g; END { exit($c == 1 ? 0 : 1) }' \
      "$mscript" || fail "mutation [$mname]: pattern found != 1 time in route script"
  elif [ "$mtarget" = "workflow" ]; then
    mworkflow="$mdir/runner-router.yml"
    cp "$WORKFLOW" "$mworkflow"
    MUT_OLD="$mold" MUT_NEW="$mnew" perl -0777 -pi -e \
      '$c += s/\Q$ENV{MUT_OLD}\E/$ENV{MUT_NEW}/g; END { exit($c == 1 ? 0 : 1) }' \
      "$mworkflow" || fail "mutation [$mname]: pattern found != 1 time in workflow file"
  else
    fail "mutation [$mname]: unknown target [$mtarget]"
  fi
  # The mutated suite must fail somewhere: a re-run of the declaration
  # check plus every case, all quiet. Any single failure kills the mutant.
  killed=0
  killer=""
  check_workflow_decl "$mworkflow" >/dev/null 2>&1 || { killed=1; killer="input declaration"; }
  if [ "$killed" = "0" ]; then
    j=0
    while [ "$j" -lt "$NCASES" ]; do
      if ! run_case "$j" "$mscript" "$mdir" 1 >/dev/null 2>&1; then
        killed=1
        killer="$(jq -r ".cases[$j].name" "$FIXTURE")"
        break
      fi
      j=$((j + 1))
    done
  fi
  [ "$killed" = "1" ] || fail "mutation [$mname] survived: no case failed"
  printf 'MUTATION KILLED [%s] by [%s]\n' "$mname" "$killer"
  i=$((i + 1))
done

printf 'ALL GREEN: %d cases pass and %d named mutations are killed\n' "$NCASES" "$NMUTS"
