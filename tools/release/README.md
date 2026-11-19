# Release-intent contract and freeze handoffs

`release-intent` reads a repository's `.github/release-intent.yaml`; its
workflow checker separately reads `.github/release-queue.policy.yml`. A
repository's tag-workflow guard and its policy remain separate.

## Before re-pinning

Before adopting changelog corrections, a consumer must add a plan-job GitHub
API provenance check authenticating the release PR independently of its title
and changed files. The planner's candidate predicate alone does not prove
authorship.

Commits already in history that combine a releasing title with a changelog
edit remain excluded from the pending set; the guard prevents new ones. Check
your range before adopting.

The landed release PR commit is the batch boundary. A fresh candidate is
verified and tagged for the subjects in its snapshot, even if another releasing
PR lands afterward. Later subjects remain in the full pending range and belong
to the next release after the candidate is tagged. A candidate whose own
release content is stale is still rebuilt instead of tagged. `candidate_fresh`
is judged against the candidate's `Before` sub-range only; a later subject is
for the next release, not evidence of staleness.

## Per-tool release stream

`streams` is optional; without it, the existing repository-wide release
grammar and configuration stay unchanged. When present, it must contain exactly
one named stream with `tag_prefix`, `changelog`, `version_files`, and `scopes`.
The top-level `tag_prefix`, `changelog`, and `version_files` must be empty.
A second stream needs a selector that is not implemented; consequently,
cross-stream scope disjointness, `stream-ambiguous`, and prefix-overlap
validation are deferred.

With a stream configured, a title belongs to it when any title scope matches a
stream scope. `feat` and `fix` require a level for that stream and must omit
it otherwise. Other types that may release need a stream scope when they carry
a level. A breaking `!` also requires a stream scope, except on a `release`
request. A `release` title always carries a level and must not have a scope.
Never-release types still forbid both markers. Every landed subject is parsed;
an invalid subject fails the plan even if it concerns another tool.

Surface names come from the release config. Each surface maps to one or more
unversioned generated file stems. Stems must be unique and contain only
lowercase letters and hyphens. `spec_freeze` defaults to false; enabling it
requires releases, at least one surface, and a `freeze_dir`. For example:

```yaml
surfaces:
  types: [types]
  api: [api, publish-request]
spec_freeze: true
freeze_dir: generated
protected_paths:
  - 'generated/*-[0-9]*'
freeze_outputs:
  - 'generated/*.json'
  - registry.yaml
  - 'mirrors/*.json'
```

The first stem in each surface's list is its primary stem. `freeze_dir` is the
repository-relative directory for numbered primary documents; it must be a
literal safe directory path without traversal, Git metadata, workflow paths,
glob characters, or a trailing slash. Repositories enabling `spec_freeze`
must configure it before adopting this tool. `freeze_outputs` is optional and
must be empty unless `spec_freeze` is enabled. Entries are
unique repository-relative paths or `path.Match` globs (where `*` does not
cross `/`). They cannot traverse, use Git pathspec characters, name Git
metadata or workflow paths, or match the changelog or a version file.
`protected_paths` entries also cannot match the changelog or a version file;
those files have their own rules. Concrete
matches are checked again for safety, including when a glob is broad. Paths
that happen to be listed in both `protected_paths` and `freeze_outputs` remain
protected; `freeze_outputs` by itself does not protect a file from content PRs.

## Contract report

The producer writes one YAML document with all fields present:

```yaml
report: 1
base: <base-commit-sha>
head: <head-commit-sha>
go_api_breaking: false
surfaces:
  - {name: types, breaking: false, change: none, ahead_on_base: false}
  - {name: api, breaking: false, change: functional, ahead_on_base: false}
```

`change` is `none`, `docs`, or `functional`; a breaking change must be
functional. The report must name every configured surface exactly once. Unknown
or missing fields, additional documents, invalid values, and mismatched
surface sets are errors, not successful checks.

```sh
printf '%s\n' 'fix(api)[patch]: update' | release-intent check-contract --report report.yaml
release-intent check-contract --report report.yaml --until HEAD --floor "${RELEASE_BATCH_FLOOR}"
release-intent check-contract --until HEAD --print-range-base --floor "${RELEASE_BATCH_FLOOR}"
```

The title form reads the config at the report's base and requires the report's
head to equal checkout HEAD. If the base has no config at all, the adopting PR
is checked using its HEAD config; a failed base read never triggers fallback.
The range form reads the first-parent range ending
at `--until`, excluding a final tag at that revision, and requires both report
endpoints to equal the computed commit SHAs. The grammar epoch and optional
floor bound the subjects, and changelog-changing commits are excluded.
The range-base-only form prints `range_base=<tag>` without judging a report or
parsing titles.

An undeclared break returns `under-declared-breaking`. With `spec_freeze: true`,
a functional change without a release returns `spec-change-needs-bump`, and
missing or unjustified configured surface scopes return `surface-mismatch`.
For a releasing title, a listed surface is justified only when it changed
functionally or was already ahead on the base; a docs-only or unchanged
surface that is not already ahead is rejected. A non-releasing docs-only
title remains valid.
Range checking can only determine whether any subject declares a break; it
does not attribute a change to an individual title.

## Freeze state and plan

The state producer writes a strict, complete document:

```yaml
status: 1
surfaces:
  - {name: types, label: Types, frozen: 0.1.0, state: clean}
  - {name: api, label: API, frozen: 0.1.0, state: ahead}
```

State is `clean`, `docs-ahead`, or `ahead`. The planner needs one entry for
each configured surface and a valid bare version.

```sh
release-intent freeze-plan --branch origin/main --state status.yaml --out freeze.yaml --floor "${RELEASE_BATCH_FLOOR}" --freeze-floor "${FREEZE_MIGRATION_SHA}"
```

`--until REV` optionally selects a first-parent candidate on `--branch`.
Both floors are optional. `--freeze-floor SHA` must be the full literal
40-character commit SHA on the selected revision's first-parent chain; it
excludes that commit and earlier subjects from freeze-level calculation only.
When migrating to generated spec freezes, set it to the **first commit that
added the working spec files**: subjects from before that migration must not
affect the first freeze.
Excluding the migration commit's own subject is also intentional.
The release range base, recovery `--floor`, grammar epoch and freeze floor
all bound the freeze subjects: the nearest boundary to the range tip wins.
The range-base and floor commits are excluded; the epoch commit is included
unless another boundary excludes it. After the first post-migration
release, the range base is newer than the migration, so retaining the freeze
floor has no further effect.

For each `ahead` surface, the command uses the highest releasing title level
that names it; no such declaration returns `surface-undeclared`. Other surfaces
are unchanged. The output is a total plan in config order:

```yaml
plan: 1
freeze_floor: <migration-commit-sha>
surfaces:
  - {name: types, label: Types, from: 0.1.0, to: 0.1.0, level: none, breaking: false, freeze: false}
  - {name: api, label: API, from: 0.1.0, to: 0.2.0, level: minor, breaking: false, freeze: true}
```

`freeze_floor` is present only when a non-empty `--freeze-floor` was passed.
The plan carries it to `release-pr --commit`, which validates the surfaces
against that bounded freeze range. Without it the existing full release
pending set is used for validation. The separate release `--floor` still
bounds the changelog and module-version calculation; the freeze floor never
shortens that release content.

The plan is a file handoff, not a shell argument list. `release-intent` does
not produce the state or perform the freeze. The consumer must validate the
plan against its current tree before writing files. **The plan is trusted
input:** the tool checks that its floor is on the first-parent chain and that
its surfaces match the resulting range, but it does not determine whether that
floor is the correct migration boundary. Whoever controls the plan controls
the freeze decision. Generate it inside the trusted release job and never
accept a plan supplied by a PR; the tool does not authenticate its origin.

Both commands return 0 for acceptance, 2 for typed rejection (`error=<code>`
on stdout and a six-part diagnostic on stderr), and 1 for a usage or read
failure. Successful contract checks print `surfaces=` and `error=`; successful
freeze plans print `frozen=` and `error=`. These machine lines are also
appended to `GITHUB_OUTPUT` when set.

## Check title and changelog

A PR other than the bot's release PR that touches the changelog must have no
release level and must not use the `release` type; for example, use
`docs(changelog): ...`.
Otherwise `check-title` rejects it with `changelog-release-title`. This also
applies to an adoption PR that adds the changelog. Normal PRs may not add a
second `[Unreleased]` block.

## Release-branch preparation

Ordinary reviewed PRs may edit changelog prose, previous sections, reference
links, and `[Unreleased]`; they may not change the ordered release headings or
protected version fields. The generator reads the source tip, inserts a new
section and carries `[Unreleased]` entries, preserving reviewed corrections.
When rebuilding an untagged version already in the file, it updates that
section without dropping existing prose or duplicating the heading. Candidate
freshness regenerates from the candidate's first parent. A normal correction
is not a candidate; only a changelog-changing release-titled commit can be
selected, and only a fresh candidate is eligible for tagging. The predicate
does not authenticate PR authorship. All changelog-changing subjects remain
outside the pending set; the title guard requires normal changelog corrections
to have no release level and not use the `release` type, so releasing code
changes need a separate PR.
Fixing a prior release date also requires the bot: its heading line is guarded.
On rebuild, generated lines match exactly; rewording a generated line in an
untagged section can cause the original line to reappear.

`release-pr --commit --branch REF [--freeze-plan FILE] [--current REF]
[--floor SHA]` generates the changelog and version-field changes, and may carry
unstaged regular files named by `protected_paths` from the worktree, including
new files. With a validated `--freeze-plan` it may also carry paths named by
`freeze_outputs`; without a plan those entries grant no write allowance. It
still refuses unrelated dirty or untracked files, staged changes, symlinks and
unsafe paths. It verifies the staged and committed bytes.

A supplied plan is strict and must match the releasing titles. For every
surface with `freeze: true`, a dirty path must match
`<freeze_dir>/<primary-stem>-<to>.*`, where the primary stem is the first
stem configured for that surface and `to` is its plan version. A missing primary
document is a `freeze-output-missing` typed rejection naming the surface and
expected prefix. The generated changelog must also change when a commit is
needed. Only `--commit` accepts `--freeze-plan`; it prints the exact generated
title and source commit as `title=` and `source=`. An existing `--current`
branch is a no-op only when its title, permitted output bytes and modes, and
generated release content all match; primary-document validation still runs.

The title guard checks release PR paths against the changelog, protected
paths, freeze outputs and protected version fields. It cannot inspect a freeze
plan, so the commit command enforces the plan gate. Other paths remain outside
the release PR's allowed scope.

Schema's freeze-writer adoption must include a repository-side coverage test
that runs or imports the real freeze writer and asserts that every path it
writes is covered by the configured changelog, version fields,
`protected_paths`, or `freeze_outputs`. This guards the hand-written list
against drifting when the writer changes.

## Pre-tag policy

The queue policy lists typed checks in verdict order:

```yaml
pre_tag_steps:
  - {id: verify-contract, check: contract}
  - {id: verify-freeze, check: freeze}
```

The closed checks are `config`, `workflows`, `contract`, and `freeze`. A
declared check must run the matching single command; `contract` needs
`check-contract --report ... --until ...`, and `freeze` needs
`schema-gen freeze --check`. An optional `nix develop -c` wrapper (or one
literal flake reference before `-c`) is the only accepted wrapper. Empty
`pre_tag_steps: []` is valid; bare string entries are not.

## Verdict inheritance

A release-only candidate inherits its exact base commit's merge-queue verdict
instead of running the full suite again:

```sh
release-intent inherit-verdict --base "$BASE_SHA" --head "$HEAD_SHA" --repo "$GITHUB_REPOSITORY" [--wait 20m]
```

`--base` and `--head` are full commit SHAs. The release config, the queue
policy and the aggregator's workflow are read at the base, so a candidate
cannot widen what counts as release content.

`inherit-verdict` reads the network and `publish-release` writes it; no other
command does. Both use GitHub REST over the standard library, never a client
library; `inherit-verdict` sends only read-only GETs. Every other command
reads git and files only; `queue-gate` reads the queue answer a workflow step
fetched.

**Release-only.** The whole `base..head` tree diff (not a merge-base range)
must modify in place the changelog and, optionally, version files whose only
difference is the configured version field. An addition, deletion, rename,
type or mode change, an unreadable version file, any other path, a diff that
does not touch the changelog, and an empty diff all mean "not release-only".
A changelog or version file that is a symlink (mode 120000) or a submodule on
either side is `not-a-regular-file`.
`protected_paths` and `freeze_outputs` never qualify: they authorize what the
release bot writes, and spec-freeze outputs must run the full suite. The
command prints `release_only=` and `reason=`; a change that is not
release-only prints `inherit=false` and exits 0.

**The base verdict.** The queue policy opts in:

```yaml
queue:
  required:
    - {context: merge-gate, workflow: tests.yml, job: merge-gate, aggregator: true}
  inherit_verdict: {context: merge-gate, full_suite_job: check}
```

Inheritance reads exactly one aggregator context per repository, the one
`inherit_verdict.context` names; it must be a required entry marked
`aggregator: true`. Peasant and schema each have a single `merge-gate`
aggregator and fit. Fairtrade's queue requires `lib` beside its `gates`
aggregator, so it needs `gates` to cover `lib` (a single aggregator) or a
later list form of `inherit_verdict` before it can opt in.

The command lists the base commit's check runs (`filter=all`, paginated) and
accepts only the aggregator context from the `github-actions` app, on the one
`merge_group` run of the aggregator's workflow whose head is the base. That
run and the aggregator's latest attempt must both be completed with
`success`; an older successful attempt does not count. A push or
pull_request run on the same commit is not an input. Anything else exits 2
with `inherit=false` and one of `base-verdict-missing`,
`base-verdict-pending`, `base-verdict-failed`, `base-verdict-ambiguous` or
`base-verdict-producer`: the gate fails closed and never falls back to
running tests. A completed run whose latest attempt has no aggregator result
of its own is `base-verdict-ambiguous`. API and read failures exit 1.

`--wait` is for a merge group built on a group still ahead of it in the
queue. It re-reads a missing or pending verdict while one more poll still
ends before the deadline:

| constant | value |
|---|---|
| poll interval (`inheritPollInterval`) | 30 seconds |
| largest `--wait` (`inheritMaxWait`) | 30 minutes |
| default `--wait` | none (0s): a pending verdict fails at once |

Schema's merge-group runs pass `--wait 20m` inside a 30-minute job.

The token (`GH_TOKEN` or `GITHUB_TOKEN`) needs `checks: read` and
`actions: read`. `GITHUB_API_URL` must be https (plain http only for a
loopback test server), a next page off that URL is never followed, and a
response over 32 MiB is an error rather than a truncated read.

**Transitivity.** The aggregator ends with the marker step set, version 1
(`intent.VerdictMarkerStepNames`, `intent.VerdictMarkersVersion`, pinned in
`internal/intent/testdata/verdict_markers.yaml`): steps named literally
`verdict-source: full`, `verdict-source: inherited` and
`verdict-source: unaffected`, in that order, exactly one of which runs per
run. A consumer that asserts its markers mirrors the names and the version;
any change to the set bumps the version. The command
reads the marker from the jobs API and prints it as `verdict_source=`, with
`inherited_from=<base>`. Any source is accepted, but `full` also requires the
full-suite job's latest attempt in that run to have passed. An inherited
verdict was itself granted only over a proven base, so every chain ends at a
run where the full suite ran (or where the path filter found no code change
over such a run).

**Bootstrap.** A base whose aggregator has no marker steps was built before
this rule. It is accepted as `legacy-full` only when the same run's
full-suite job passed on its latest attempt; a pre-marker run that skipped
the full suite is `base-verdict-failed`.

`check-workflows` enforces only what the tool reads back
(`inherited-verdicts-are-recorded`): the full-suite job exists and the
aggregator needs it, the aggregator's last steps are the marker set and the
job has no `continue-on-error`, and a
job the aggregator needs runs `inherit-verdict` in a step with no step-level
`if` and no `continue-on-error`. The full-suite job's literal name and the
token scopes are checked when the command runs, where a fault fails closed.
The aggregator job may not carry `continue-on-error` either: a failed
aggregator would report success, and nothing at run time can tell. A wrong
`full_suite_job` (or a renamed full-suite job) passes policy validation as
long as the job exists; it is caught at run time, as `base-verdict-failed`
on the first release candidate whose base recorded a `full` (or pre-marker)
verdict, because no job of that name passed in the base run.

Markers are kept, rather than inheriting only from bases whose full suite
ran, because a docs-only or release base is common: without a recorded
source, every release built on one would lose inheritance.

## Publishing a release

```sh
"${RUNNER_TEMP}/release-intent" publish-release --tag "$TAG" --repo "$GITHUB_REPOSITORY" \
  --assets dist/assets --notes dist/release-notes.md --prerelease "$PRERELEASE" --latest "$LATEST" \
  [--require-immutable]
```

This is the one way release assets reach a GitHub Release; the workflow
policy requires the publish job shape in the top-level README ("Prepared
Release assets"). `--prerelease` and `--latest` take `true` or `false`, never
both `true`. The command assumes it is the only run for its tag: a second
run overlapping it would create, verify and publish its own release. The
policy therefore requires the workflow or the publish job to set
`concurrency: {group: release-${{ github.ref_name }}, cancel-in-progress: false}`
(`github.ref` is accepted too; every `concurrency` it sets must write out
`cancel-in-progress: false`). The token is `GH_TOKEN` (or `GITHUB_TOKEN`) with
`contents: write`.

1. **Manifest, before any request.** `--assets` holds at least one entry, all
   flat regular files (no symlink, no subdirectory) whose names GitHub keeps
   (`[A-Za-z0-9._+-]`, no leading or trailing period); otherwise
   `release-assets-invalid`. Each file's name, size and SHA-256 is read once.
   The notes are a non-empty regular file outside `--assets`.
2. **List every release**, drafts included, 30 per page: a release object
   embeds its assets (a 352-asset schema release is about 580 KiB), so 100 per
   page would pass the response cap. Reaching the page cap with another page
   left is `release-list-truncated`. The page URLs are built from the listing
   path: GitHub's `rel="next"` link names the repository by numeric id, so it
   is read only as "another page follows".
3. **Decide.** A draft for the tag is `draft-exists`, naming each id and
   `gh api -X DELETE repos/<repo>/releases/<id>`; the command never deletes a
   release it did not create. Two published releases are `release-ambiguous`.
   One published release is the rerun path: it is compared, never changed.
   Exactly the manifest (and immutable under `--require-immutable`) prints
   `state=already-published` and exits 0; otherwise
   `published-release-incomplete` (missing, extra, not uploaded, still a
   draft), `published-release-differs` (same name, other size or digest) or
   `release-not-immutable`, each fixed forward with a new tag.
4. **Tag.** With no release, `refs/tags/<tag>` must exist (`tag-missing`):
   publishing a draft for a missing tag would create it at the default branch.
5. **Create, upload, verify, publish, verify**, all by release id: the draft
   (`release_id=` is printed at once), one upload per file to
   `uploads.github.com`, then the draft's assets compared with the manifest
   (`draft-incomplete`), the publish (`draft: false`, `prerelease`,
   `make_latest`), and the published release compared again. A publish request
   that fails is read back by id: still a draft is cleaned up, published is
   verified.
6. **Cleanup.** Any failure before the publish deletes this run's own draft by
   id. GitHub answers a delete of a release with hundreds of assets with 502
   after about ten seconds and may keep it, so a server error or a lost answer
   makes the command delete that draft's assets one by one, then the empty
   draft, and read it back until it is gone. If that fails it prints the id
   and the delete command.

Exit 0 prints `release_id=` and `state=` (`published`, `already-published`,
`rehearsed`); exit 2 is a typed rejection with `error=` and the six-part
diagnostic; exit 1 is a usage, API or transport failure, with `release_id=` on
stdout once a draft exists. The lines are appended to `GITHUB_OUTPUT` too.

**The writer** (`cmd/release-intent/githubwriter.go`) maps every request to a
closed allow-list before sending it: list releases, read the tag's ref,
create a release, upload an asset, read a release, list its assets, update a
release, delete a release, and delete an asset, all on `--repo` and its
`--tag`. Update, upload and delete name only a release this run created, and
asset delete only an asset listed on such a release. Anything else is refused
as a programming error. The API base is `https://api.github.com` (uploads to
`https://uploads.github.com`) or, for tests, a plain-http loopback server; a
response over 32 MiB is an error; the token travels only in the
`Authorization` header and is never printed.

**Rehearsal.** `--rehearse` runs steps 1-2, refuses unless the tag starts with
`rehearsal-` and neither a release nor a ref exists for it
(`rehearsal-refused`), then creates, uploads and verifies a draft and deletes
it by id; it never publishes, and prints `state=rehearsed`. It proves GitHub's
real draft semantics at full scale from a maintainer's machine, and the
workflow policy refuses it. Remove it once schema and peasant have each
published one release through the command.
