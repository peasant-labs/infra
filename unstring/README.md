# unstring

`unstring` guards a repository against stringly-typed values: a closed-set
member, a test id, an endpoint path, or a data path written as a bare string
where a typed value belongs. It scans the roots named in `.unstring.yaml` with
ast-grep, compares the hits with a committed per-file baseline, and fails when
the tree is not the baseline.

```sh
go run github.com/peasant-labs/infra/unstring/cmd/unstring@vX.Y.Z check
go run github.com/peasant-labs/infra/unstring/cmd/unstring@vX.Y.Z update
go run github.com/peasant-labs/infra/unstring/cmd/unstring@vX.Y.Z init
go run github.com/peasant-labs/infra/unstring/cmd/unstring@vX.Y.Z show rules
```

Pin the module version in one place (a `Makefile` or `package.json`). The tool
requires ast-grep **0.51.2** exactly (`ast-grep` on `PATH`, or `--ast-grep`).
Any other version stops the run before a scan.

## Release

The Go module uses the independent `unstring/v` tag namespace. Release intent
is carried by PR titles: for example `feat(unstring)[minor]: add a rule` or
`fix(unstring)[patch]: correct a diagnostic`. An unrelated `feat` or `fix`
has no bracket. Rule-match and required ast-grep changes are minor; a
diagnostic-only change is patch. Describe rule effects and measured consumer
counts in the PR body.

The serialized release batch rebuilds `automation/release` and its changelog
from landed intents, then arms it through the native merge queue. Do not edit
`CHANGELOG.md` or the bot branch by hand. The App pushes an annotated tag only
after the release PR lands and the pre-tag checks pass. The tag-triggered
workflow verifies the tag and creates the GitHub Release. Confirm the module
is available via `go list -m github.com/peasant-labs/infra/unstring@vX.Y.Z`.
Never move or delete a tag; fix a failed release forward.

Before enabling writes, the maintainer configures the releaser App client-ID
variable `PEASANT_RELEASER_CLIENT_ID` and private-key secret
`PEASANT_RELEASER_APP_PRIVATE_KEY`, enables auto-merge, and sets squash commit
titles to the PR title. Update the main branch queue ruleset to require both
`release-tool` and `title-guard`, and compare it with
`.github/release-queue.policy.yml`. Leave `RELEASE_BATCH_MODE` unset while
dispatching `release-batch` and checking its plan; after verifying the queue
and a merge-group build, set it to `tag`. `RELEASE_BATCH_FLOOR` is reserved
for recovery from an unparseable landed subject. A forced release uses the
`cut-release` dispatch with `patch` or `minor`, never a direct branch push.

The same `unstring/vX.Y.Z` tag publishes `eslint-plugin-unstring`. The release
PR writes `unstring/CHANGELOG.md` and the version field in
`unstring/eslint-plugin/package.json`, the npm version mirror of the computed
tag; the Go module takes its version from the tag. The npm workflow verifies
the tag against the default branch and checks that the package version matches.
`verify-tag` refuses prerelease tags such as `unstring/vX.Y.Z-rc1`, so they
cannot reach npm's `latest` dist-tag. The package ships `src`, README and
LICENSE; it does not ship a preset.

### First npm publish

Before enabling the first release, the maintainer must complete these gates:

1. Ensure the required schema release is available, and create the GitHub
   Actions environment `npm-publish` in `peasant-labs/infra`. Restrict its
   deployment tags to `unstring/v*`; require reviewers if a second maintainer
   is available.
2. The npm package owner checks that `eslint-plugin-unstring` is available
   and creates a granular npm token permitted to create and publish that
   package, with package read/write access restricted to that package if npm
   supports it, bypass 2FA for CI, and a seven-day expiry as the backstop.
   Never use a classic or organization-wide token. If npm cannot scope a
   token to the not-yet-created package, the owner must use the narrowest
   creation permission npm permits, and revoke it immediately after bootstrap.
3. Store the token as the `NPM_TOKEN` environment secret on `npm-publish`,
   never as a repository secret. The tag-triggered
   `npm-publish.yml` uses it only when present; a tag publishes both the Go
   module and the npm package. Do not create or move a tag manually.
4. After the first successful publish, the npm package owner registers a
   Trusted Publisher for repository `peasant-labs/infra`, workflow
   `npm-publish.yml`, and environment `npm-publish`.
5. Revoke the npm token and delete the `NPM_TOKEN` environment secret immediately.
   Subsequent releases use OIDC only; do not restore a token as a fallback.
   Verify the package version and provenance with `npm view
   eslint-plugin-unstring@X.Y.Z version dist.attestations`.
6. Confirm `NPM_TOKEN` is absent from the environment using `gh secret list
   --repo peasant-labs/infra --env npm-publish` or the environment settings.
   Also check repository secrets with `gh secret list --repo peasant-labs/infra`
   to confirm there is no fallback token.

The release-tool SHA appears in five checkouts across `release-batch.yml`,
`release.yml`, `npm-publish.yml`, and `title-guard.yml`; all five must move together.
`cut-release.yml` is the fourth release workflow but does not check out the tool.

### Recovery

`unstring/v0.1.0` is published as the first plugin release. These recovery
rules apply to subsequent releases.

- Only the `peasant-labs-releaser` App (Integration 3988034) can bypass the
  `Protect release tags` ruleset to create release tags. Human tag pushes are
  refused; do not change the ruleset to work around a failed batch.
- If a plan returns `action=none` while a release is expected, confirm
  `RELEASE_BATCH_MODE=tag` and whether a later non-releasing fix moved the tip.
  Do not treat `none` as success and do not move a tag; if the release cannot
  be tagged at the intended commit, cut a new release that includes the fix.
- npm publishing uses Trusted Publishing (OIDC). The `npm-publish` environment
  and Trusted Publisher registration are one-time maintainer setup; steady
  state needs no long-lived `NPM_TOKEN`. The version file is
  `unstring/eslint-plugin/package.json`.

### Exercise the planner without a tag

Run from the repository root with Go and Git installed. This clones into a
scratch directory and copies existing tag references; it creates no tag and
pushes nothing. The exercise uses the existing `unstring/v0.0.4` as its base.
`close-pr`, `open-pr`, and `verify` print those values as `action=`; the queue
command prints `gate=hold` (hold is a queue decision, not a planner action).
The release-commit step writes only in the scratch clone.

```bash
set -euo pipefail
repo="$(git rev-parse --show-toplevel)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/unstring-no-tag.XXXXXX")"
go build -C "$repo/tools/release" -o "$scratch/release-intent" ./cmd/release-intent
bin="$scratch/release-intent"
git clone --quiet --no-local "$repo" "$scratch/repo"
cd "$scratch/repo"
export GIT_AUTHOR_NAME=Exercise GIT_AUTHOR_EMAIL=exercise@example.invalid
export GIT_COMMITTER_NAME=Exercise GIT_COMMITTER_EMAIL=exercise@example.invalid
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
# The clone copies existing tags; no tag is created or pushed.
tree=$(git rev-parse 'HEAD^{tree}')
base=$(printf 'ci(unstring): adopt release automation\n' | git commit-tree "$tree" -p "unstring/v0.0.4^{commit}")
git switch -q -c exercise "$base"
printf '\nclose-pr:\n'
"$bin" plan --branch exercise --mode tag
next=$(printf 'feat(unstring)[patch]: exercise the planner\n' | git commit-tree "$tree" -p "$base")
git update-ref refs/heads/exercise "$next"
printf '\nopen-pr:\n'
"$bin" plan --branch exercise --mode tag
git switch -q -c automation/release exercise
"$bin" release-pr --commit --branch exercise
candidate=$(git rev-parse HEAD)
git switch -q exercise
git merge --ff-only -q "$candidate"
printf '\nverify:\n'
"$bin" plan --branch exercise --mode tag
cat > "$scratch/queue.json" <<'JSON'
{"data":{"repository":{"mergeQueue":{"entries":{"totalCount":2,"nodes":[{"position":1,"pullRequest":{"number":11,"title":"fix(unstring)[patch]: queued fix"}},{"position":2,"pullRequest":{"number":12,"title":"release[patch]: unstring/v0.0.5"}}]}}}}}
JSON
printf '\nhold:\n'
"$bin" queue-gate --queue "$scratch/queue.json" --release-pr 12
```

## What it scans

A run reads `.unstring.yaml` (or `--config`) and scans only the roots of the
languages that section names: TypeScript and JavaScript under `ts`, Go under
`go`. At least one of those sections is required. Static rules are embedded in
the tool. The two closed-set rules are generated on every run from a catalog
that is never written back to the repository.

The catalog is rebuilt from the scanned repository plus, when `schema.module`
is set, the pinned schema module. Members come from six source kinds: a
`closed()` call, a frozen record table, a TypeScript string-literal union, a
JSDoc string-literal union, a Go typed string constant, and the schema module.
The Go catalog uses the version required by `go.mod`. The TypeScript catalog
uses the exact `@peasant-labs/schema` version in the TypeScript root's
`package.json`. The tool fetches that module with `go mod download` and
harvests the same kinds of members from it. A shape that is not one of those
sources is not a catalog member.

`unstring show catalog` prints the members and where they are declared.
`unstring show rules` prints every rule id.

## Config

Paths are relative to the directory that holds `.unstring.yaml`, with `/`
separators. A `\` separator, an absolute path, and a `.` or `..` segment are
refused.

| Key | Default |
|---|---|
| `version` | required; only `1` |
| `baseline` | `.unstring-baseline.yaml` |
| `ts.roots` | none; the `ts` section is omitted when there are no TypeScript or JavaScript roots |
| `go.roots` | none; the `go` section is omitted when there are no Go roots |
| `typed.goClosedSetLiteral` | `false` |
| `typed.goMountLiteral.enabled` | `false` |
| `typed.goMountLiteral.routers` | `github.com/go-chi/chi/v5` |
| `typed.goMountLiteral.tests` | `exclude`; the other value is `include` |
| `schema.module` | none; the catalog is the repository only |
| `endpoints.prefixes` | `/api/v1` |
| `endpoints.callees` | none |
| `endpoints.mountFiles` | none; the mount-file rule is omitted |
| `dataDirs` | `testdata`, `fixtures`, `__fixtures__`, `captures` |
| `exempt` | none |

`endpoints.prefixes` are the API path prefixes the endpoint rules look for.
`endpoints.callees` are TypeScript functions whose first argument is an API
path. `dataDirs` are single directory names, not paths. `exempt` is a list of
`{rule, paths, reason}` entries. `paths` are globs (`*`, `?`, `[…]`, and `**`
for any number of directories). A path with no wildcard also matches every
file below it. `reason` is required.

```yaml
version: 1
baseline: .unstring-baseline.yaml
ts:
  roots: [web]
go:
  roots: [server]
schema:
  module: github.com/peasant-labs/schema
endpoints:
  prefixes: ["/api/v1"]
  callees: [api]
  mountFiles: [server/internal/router/*.go]
dataDirs: [testdata]
exempt:
  - rule: "*"
    paths: [server/gen/**]
    reason: generated output, regenerated from the queries
```

## Mount files

`endpoints.mountFiles` lists the Go files that mount routes. An entry is an
exact path or a glob (`*`, `?`, `[…]`, including `**`), for example
`server/internal/router/*.go`. The tool does not expand the glob. It writes
each entry, quoted and unchanged, into the mount-file rule's `files:` list,
and ast-grep matches it. A glob that matches no file yields no hits and no
error, exactly like a missing exact path; when the mount rule reports nothing,
check the pattern.

A route-mounting file has to be listed. A group-relative argument such as
`"/auth/me"` carries no API prefix, so the prefix rule does not see it. The
mount-file rule flags every `"/…"` string argument in the listed files. The
mount-file rule ignores `_test.go` files, including those a glob lists. The
same call in a file that is not listed is not a mount. The optional typed
mount check below finds mounts by the router's type instead, in every file.

## Baseline

The baseline is a count of allowed hits per rule and file. `init` writes it
once, when no baseline file exists. `check` compares the tree with it and
fails when:

- a file's count for a rule is above its entry (raised);
- a file with hits has no entry (new file);
- an entry allows more hits than the tree now has, including a file that is
  gone (stale);
- the header's `tool` or `ast-grep` line does not match the running tool and
  ast-grep 0.51.2 (header mismatch);
- a marker the tool cannot apply.

A header mismatch does not skip the count comparison, and it never allows a
count to rise. `update` only lowers a count, removes an entry with no hit
left, and moves an entry across a git rename whose count does not rise. If any
count would rise or any file would be added, it writes nothing. A successful
`update` without `--lang` rewrites the header to the running tool and
ast-grep 0.51.2. Do not edit or hand-merge the file; after a merge, take the
incoming baseline and run `update`.

The header is the record of which tool last verified the counts. Before any
scan, the tool checks that the ast-grep binary reports exactly 0.51.2 and
refuses any other version.

## Exemptions

An `exempt` entry removes one rule, or every rule, from the files its paths
match.

`rule: "*"` and the two closed-set rules (`closed-set-literal`,
`go-closed-set-literal`) are accepted only when every matched file carries a
`Code generated … DO NOT EDIT` header. Those two rules also accept no
`unstring-allow` marker: the fix for a set value is the typed value.

An exemption of any other rule is a review decision. The tool does not apply
that header check to it, and the exemption is not a way to keep a hand-written
literal: the file should still be generated. Every entry needs a reason.

## Optional typed Go check

To opt in, add:

```yaml
typed:
  goClosedSetLiteral: true
```

On `check`, this loads the configured Go roots with Go type information and
reports comparisons and switch cases that use a literal matching exactly one
package-level constant of the receiver's named string type. Plain strings,
interfaces, untyped values, types without constants, unmatched values and
ambiguous values are silent. Package loading errors fail the check; the Go
packages and their test packages must build in the environment running the tool.
Go `//line` directives do not change which files the pass scans.
Findings name the physical file and line, even when a `//line` directive maps them elsewhere.
An unexported constant in another package produces a finding without a
replacement identifier: export the constant or move the comparison.

These `go-typed-closed-set-literal` findings are not structural baseline
entries and cannot be suppressed by baseline edits or markers. `init` and
`update` only manage the structural baseline; enabling this check requires
fixing typed findings before `check` passes.

## Optional typed Go mount check

To opt in, add:

```yaml
typed:
  goMountLiteral:
    enabled: true
    routers: [github.com/go-chi/chi/v5]  # the default
    tests: exclude                       # the default; or include
```

On `check`, this loads the configured Go roots with Go type information and
reports a route-mount call whose pattern argument is a string literal, as
`go-typed-mount-literal`. A call is a mount when its method is declared in a
package listed under `routers` and is one of `Get`, `Post`, `Put`, `Patch`,
`Delete`, `Head`, `Options`, `Connect`, `Query`, `Trace`, `Handle`,
`HandleFunc`, `Method`, `MethodFunc`, `Route` or `Mount`. The method is
resolved with `go/types`, not by name, so:

- a receiver of chi's `Router` interface matches as well as `*chi.Mux`. That
  covers the router inside a `Route` callback and the one `With` returns;
- a type that embeds a router matches through the promoted method;
- a type of your own with a `Get` or `Route` method does not match, and
  neither does `*http.ServeMux` unless `net/http` is listed.

The pattern is the first argument, or the second for `Method` and
`MethodFunc`. It is reported when it is a string literal, or literals joined
with `+`, that starts with `/` or with an HTTP method and `/` (`"GET /x"`).
A pattern that is a constant, a variable or a call, such as `rt.Pattern()`, is
not reported; neither is a wrapper that forwards its parameter to the router.
The literal is reported where it is written. A `Route` or `Mount` group
prefix is a mount in its own right, and a pattern inside the group is reported
as written, relative to the prefix; the check never composes the full path.

`_test.go` files are skipped unless `tests: include`: a test usually builds
its own router, which is fixture code and not a production mount. Files are
classified by their physical name, so a `//line` directive does not move a
file in or out, and findings name the physical file and line. A listed router
package that no package under the roots imports fails the check, because the
check would otherwise match nothing.

Like the closed-set check, these findings are not structural baseline entries
and cannot be suppressed by baseline edits or markers. Both typed checks share
one package load. The structural `go-endpoint-literal` mount-file rule stays
in place; this check does not replace it.
