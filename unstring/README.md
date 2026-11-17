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
same call in a file that is not listed is not a mount.

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
