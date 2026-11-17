# Release-intent contract and freeze handoffs

`release-intent` reads a repository's `.github/release-intent.yaml`; its
workflow checker separately reads `.github/release-queue.policy.yml`. A
repository's tag-workflow guard and its policy remain separate.

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
floor bound the subjects, and changelog-changing release commits are excluded.
The range-base-only form prints `range_base=<tag>` without judging a report or
parsing titles.

An undeclared break returns `under-declared-breaking`. With `spec_freeze: true`,
a functional change without a release returns `spec-change-needs-bump`, and
missing or unjustified configured surface scopes return `surface-mismatch`.
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
release-intent freeze-plan --branch origin/main --state status.yaml --out freeze.yaml --floor "${RELEASE_BATCH_FLOOR}"
```

`--until REV` optionally selects a first-parent candidate on `--branch`.
For each `ahead` surface, the command uses the highest releasing title level
that names it; no such declaration returns `surface-undeclared`. Other surfaces
are unchanged. The output is a total plan in config order:

```yaml
plan: 1
surfaces:
  - {name: types, label: Types, from: 0.1.0, to: 0.1.0, level: none, breaking: false, freeze: false}
  - {name: api, label: API, from: 0.1.0, to: 0.2.0, level: minor, breaking: false, freeze: true}
```

The plan is a file handoff, not a shell argument list. `release-intent` does
not produce the state or perform the freeze. The consumer must validate the
plan against its current tree before writing files.

Both commands return 0 for acceptance, 2 for typed rejection (`error=<code>`
on stdout and a six-part diagnostic on stderr), and 1 for a usage or read
failure. Successful contract checks print `surfaces=` and `error=`; successful
freeze plans print `frozen=` and `error=`. These machine lines are also
appended to `GITHUB_OUTPUT` when set.

## Release-branch preparation

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
