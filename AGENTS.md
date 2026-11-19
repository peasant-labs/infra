# Agent instructions

This repository is the organisation's shared infrastructure: **cloud control
plane**, **reusable CI**, and **container image maintenance**. Anything another
`peasant-labs` repository calls as a workflow, trusts as a build input, or
deploys through belongs here rather than in a personal repository.

| Area | Lives in | Consumers |
|---|---|---|
| Cloud control plane | `stacks/`, `modules/terraform/`, `scripts/` | nobody imports it; it owns production |
| Reusable CI | `.github/workflows/runner-router*.yml` | every routed repository |
| Container images | `runner-image/`, `.github/workflows/runner-image.yml` | the NixOS runner module, every routed job |
| Runner pool module | `modules/nixos/`, `flake.nix` | any host that imports `nixosModules.default` |
| Release tool | `tools/release/`, `.github/workflows/release-tool.yml` | every repository that releases through the merge queue |
| Unstring guard | `unstring/` (Go module `github.com/peasant-labs/infra/unstring`), `.github/workflows/unstring.yml` | fairtrade, village and peasant, each at a pinned `unstring/vX.Y.Z` |

## Cloud control plane

### Boundaries

- Keep `village-production` and `pkgs-production` as separate Terraform roots,
  HCP Terraform workspaces, credentials, GitHub environments, and concurrency
  groups.
- Never commit Terraform state, plans, credentials, account IDs, zone IDs,
  signing material, R2 S3 keys, Railway variables, or Village transcript KEKs.
- Never create secret-bearing R2 credentials through Terraform. Provider secret
  values would be retained in state.
- Never add secret-bearing Terraform attributes. Saved deployment plans are
  short-lived GitHub artifacts and can contain complete resource values.
- Railway is externally managed. Do not add the community Railway provider or
  import the working production environment without a separately reviewed
  decision.
- Preserve `prevent_destroy` on production buckets and the package custom domain.
- Do not add an automated destroy workflow.

### Versions

- Terraform CLI is exactly `1.15.8` in roots and shared modules.
- Cloudflare provider is exactly `5.22.0` in every root and shared module.
- Regenerate and commit every root's `.terraform.lock.hcl` when a provider pin
  changes.

### Validation

Run `make check` before committing. Tests must use Terraform mock providers and
must not contact production services. Add separate `.tftest.hcl` fixtures for
new stack contracts rather than embedding test-only resources in production
configuration.

## Reusable CI

`runner-router.yml` is the single source of truth for pool routing. Callers use
it as a job and read the label array from its output; they never inline their own
runner-availability probe.

- **Never copy the routing block into a consuming repository.** Duplicated
  probes drift, and a silent fallback must never be invisible.
- Every caller must pass a `fallback` label set, so a run never queues on a
  machine that is off. A missing `RUNNER_STATUS_TOKEN` is notice-level, not an
  error: fork pull requests legitimately have no secrets.
- Keep the report step. Without it a fallback to the paid runner looks
  identical to intended behaviour.
- The default pool labels are `self-hosted, linux, x64, microvm`; the router
  reads the dispatcher heartbeat, not the online runner list. Infra's own
  callers use the local reusable workflow; external callers pin a reviewed SHA.
- GitHub Actions use immutable commit SHAs with a version comment.

## Container images

`runner-image/` is the recipe for `quay.io/peasant-labs/github-runner`, the
image the organisation's self-hosted pool runs. **It is a trust anchor, not a
convenience artifact**: every routed repository ultimately executes code from
it, which is why it lives here and not in a personal dotfiles repository.

### Invariants

- The image is signed **keylessly** with the identity
  `https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main`.
  Consumers verify that exact identity.
- **Digest and signer move together.** A consumer pins both. Switching one
  without the other makes pull-and-verify fail and no runner starts.
- **Never pin a mutable tag.** Publishing produces a new digest every build; a
  tag push is not a re-publish.
- The publishing workflow resolves the digest from the served manifest bytes
  and requires the registry to resolve that digest **before** signing. Quay
  re-serialises per `Accept` media type and the converted form is not addressable
  by digest, so neither the `Docker-Content-Digest` header nor a client-side
  `RepoDigests` value is trustworthy on its own.
- Pin every build input: the base image digest, the dated apt snapshot, the
  exact apt versions, and the SHA-256 of each downloaded archive. The build
  verifies each download and fails closed on a mismatch.
- `UBUNTU_SNAPSHOT` has no upstream listing API, so it is a manual bump. Move it
  together with the base digest and the apt version list, as the Containerfile
  header says.
- Publishing is **manual dispatch on purpose**. A build that is not deliberate
  should not move the digest every consumer pins.
- Consumers **deploy**; they never rebuild. The NixOS module that pulls,
  verifies, and runs the image is machine configuration and stays in the
  desktop's dotfiles repository.


## The runner-pool module

`modules/nixos/services/github-runner` deploys the pool on a host. It is
published as `nixosModules.default` so any configuration can consume it:

```nix
{
  inputs.infra.url = "github:peasant-labs/infra";

  outputs = { nixpkgs, infra, ... }: {
    nixosConfigurations.desktop = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        infra.nixosModules.default
        ({ ... }: {
          CUSTOM.services.github-runner = {
            enable = true;
            tokenFile = config.sops.secrets.runner-token.path;
            count = 4;
          };
        })
      ];
    };
  };
}
```

There is deliberately no `homeModules` output. The pool is a system concern:
image pull, the systemd user units, and the podman socket are all host-level, so
a home-manager consumer would gain nothing from it.

### Invariants

- **The module must not depend on any other repository's module.** It sets the
  nixpkgs podman options it needs itself rather than reaching into a
  host-specific wrapper namespace, so it evaluates in a configuration that
  imports nothing else. Verified by evaluating it standalone.
- `imageRef` and `imageSigner` are **options with the peasant-labs defaults**,
  not constants. A host running its own pool overrides both.
- **Never publish a runner image that the module's default pin cannot reach.**
  The default digest is what a zero-configuration host pulls; a new publish
  changes it, and the default must follow.
- **A host that disables this pool must not lose sight of the label it leaves
  behind.** The dispatcher warns while a host that disabled the pool still
  advertises a label the pool declared. Removal waits for the last caller and
  is documented in `docs/runner-pool.md`.

### Dependency updates

`renovate.json5` tracks the Containerfile pins that have an upstream feed. Those
pins only update automatically while the Renovate app is enabled on this
repository — if it is not, they go stale silently and the snapshot/version
drift has to be found by hand.

### Ad-hoc load and flake experiments

Run them inside a transient systemd unit (`systemd-run --user --wait --collect
-p RuntimeMaxSec=... -p KillMode=control-group bash -c '<load + test loop>'`).
The unit's cgroup is the cleanup boundary: a parent death cannot leave load
generators running on a pool host.

## The release tool

`tools/release` is a separate Go module that builds `release-intent`: the
title grammar, version arithmetic, release planning, and the merge-queue
workflow policy checker. Consumers check out this repository at a full commit
SHA and build `./tools/release/cmd/release-intent`; they never import it.

- **Every commit on `main` is a possible pin.** Keep the module building and
  its tests green on each one; `release-tool.yml` has no path filter so it can
  be a required check.
- **The module depends on no other `peasant-labs` module.** Its test support
  lives in `tools/release/internal/testcase`.
- **`inherit-verdict` is the only command that reads the network.** It uses
  read-only GitHub REST GETs over the standard library (`githubapi.go`), never
  a client library, and fails closed: a release-only candidate whose base
  commit has no successful, marker-recorded merge-queue verdict is rejected,
  never sent back to the test suite. The release-only set is the changelog and
  version fields only; never widen it with `protected_paths` or
  `freeze_outputs`. The marker step names are a versioned set
  (`intent.VerdictMarkersVersion`); change them only with a version bump. The rule is in the
  [README](tools/release/README.md#verdict-inheritance).
- **Changelog body corrections go through a normal reviewed PR.** Prose,
  reference links, and the `[Unreleased]` block are editable; ordered release
  headings and protected version fields stay bot-only; a normal PR may not add
  a second `[Unreleased]` block. A PR other than the bot's release PR that
  touches the changelog must
  have no release level and must not use the `release` type (for example,
  `docs(changelog): ...`); otherwise `check-title` rejects it with
  `changelog-release-title`. This also applies to an adoption PR that adds
  the changelog. The generator carries
  `[Unreleased]` entries from its input into the new section. Peasant's
  [0.18.0](https://github.com/peasant-labs/peasant/tree/v0.18.0) and
  [0.18.1](https://github.com/peasant-labs/peasant/tree/v0.18.1) were tagged
  but never published; 0.18.2 followed. Release preparation reads the source
  tip and preserves reviewed corrections; candidate freshness reads that
  candidate's first parent. Only a changelog-changing release-titled commit
  can be a candidate, and changelog-changing subjects stay out of the pending
  set, so keep releasing code changes separate from corrections. The title
  and changelog predicate identifies a candidate; it does not authenticate
  the PR's author. Consumers must add the
  [plan-job provenance check](tools/release/README.md#before-re-pinning) before adopting.
- **The queue policy file is `.github/release-queue.policy.yml`, top-level key
  `queue:` only.** `policy.DefaultPath` owns that path. Job-graph rules for a
  tag-triggered release workflow stay in schema's release-guard and its own
  policy file; do not define a rule in both.
- **The workflow checker confines credentials; it does not read shell.** A
  job that may hold a credential runs only `policy.AllAllowedActions`, the
  plumbing templates (`policy.AllPlumbingTemplates`) and the credential
  templates (`policy.AllCredentialTemplates`); apart from the reviewed-script
  exception below, all other work runs in an earlier job without a credential
  and hands over through job outputs and an artifact. Never widen it by
  teaching a reader another spelling of a write, or by letting a general step
  back into the credential job: add a typed
  template, and say in the PR why the existing ones do not fit. The README's
  "What a consumer workflow may do with a credential" is the consumer-facing
  summary; keep it in step. A credential job runs on a literal
  `policy.AllRunnerLabels` label and names its bot PR by its bot branch. A
  tag-triggered publish job needs a tag-verify job, confined the same way: only
  the `policy.AllVerifySteps` shapes, `policy.AllVerifyActions` with
  `policy.AllVerifyInputs`, and `policy.AllVerifyEnvs` at their fixed values.
  A tag-triggered publish job's `if` calls no status function and reads no
  `needs.<job>.result`, and the tag-verify job carries no job-level `if`.
  A listed comment job instead targets the triggering PR, mints with explicit
  contents-read, issues-write and pull-requests-write on one literal
  repository, and may run only the comment templates. A reviewed script uses
  the isolated default-branch checkout and a disjoint evidence directory; its
  policy-named user PAT is a credential confined to the comment step env.
  This is an accepted trust exception, not proof that the script or
  PR-controlled evidence is safe. A comment job whose workflow triggers on
  `workflow_run` (for fork and Dependabot PRs) is typed more tightly: the
  trigger is `workflow_run` alone for one named producer with `types:
  [completed]`, the job's own `if` gates on `github.event.workflow_run.event ==
  'pull_request'`, it downloads only the triggering run's artifact, mints no
  App token, runs only the reviewed script with the PAT in that step's env,
  and every job of a workflow that names `workflow_run` is a listed comment
  job (a deliberate scope limit; a probe job would need its own typed class).
  A missing artifact fails closed; a stale run is the script's exit 0.
  `pull_request_target` stays refused in every comment workflow. The
  release-assets action takes only the build job's `release-dist` handoff and
  the triggering tag. Its gate is either `view.success || create.success`
  (the existing published-create flow) or `view.failure && create.success`
  (the immutable flow: verified draft create, asset upload, then a gated
  `--draft=false` publish edit). The action requires literal `draft: true`
  in the immutable flow; in the published-create flow `draft` must be absent
  or literal `false`. `prerelease` always reads a needed job's output.
  `make_latest` must do so in the published-create flow; it may be omitted in
  the draft flow, and should be omitted because drafts cannot be marked latest.
  If present there it still reads a needed job's output. The immutable flow's
  action reads `prerelease` from a needed release-kind job. Its publish edit
  names the current repository explicitly with exactly one `--repo` or `-R`,
  passes `--draft=false`, and binds `--prerelease` and `--latest` to that same
  job's declared outputs, either directly or by matching the job's own output
  comparison. The checker does not validate the producer's computation.
  An existing release makes the immutable flow a no-op on rerun. A failed draft
  is a burned release; the fix is forward. The checker cannot inspect the
  action's implementation: verify that its exact pin finds the existing draft
  and honors `draft: true` while uploading. The
  `--print-required` mode prints the contexts but retains the policy verdict.
- **A release tag is checked before it leaves the release job.** The release
  workflow triggers only on push, schedule and workflow_dispatch, and the
  release job's `if` proves the run is on the default branch with
  `github.ref == format('refs/heads/{0}', github.event.repository.default_branch)`
  as one conjunct. Between the tag fetch and the tag push, the
  `check-tag-ancestor` plumbing template runs `git merge-base --is-ancestor
  "refs/tags/${TAG}" "${GITHUB_SHA}"` under the push's own `if`; the push's
  `if` calls no status function, so a failed check skips it. The check and
  push share one `if`, and it may compare only a completed planning job's
  outputs: it may not read `steps`, `env`, `job` or a function, because GitHub
  evaluates each step's `if` at that step's own time and the same text can
  skip the check and then run the push. A repository
  ruleset that lets only the releaser App create release tags is the operator
  step, and it covers every releasing repository including fairtrade.
- **A no-`continue-on-error` clause refuses the key, not a value.** The
  title-guard job and its steps, every step `release-actions-gated` gates, the
  tag-verify job, and the tag path (the ancestry check, the tag push and the
  release job), plus a comment job and its comment steps, the release-assets
  action and its publish job, refuse the `continue-on-error` key whatever its
  value. GitHub
  evaluates the key as a value, and the documented
  `continue-on-error: ${{ fromJSON('true') }}` form makes a failed check or
  job report success, so a literal comparison would be a bypass.
- **Read a run step that must be exactly one command through
  `singleCommand`** (`credential_command.go`). The credential templates, the
  plumbing templates, the pre-tag checks and the tag-verify job all use it. Do
  not add another way to read a run block for a step whose exit status or
  effect the policy depends on.
- **Pre-tag policy entries are `{id, check}` mappings.** The check is one of
  `config`, `workflows`, `contract`, and `freeze`. Only the exact declared
  check may run, optionally under one `nix develop -c` wrapper; an empty list
  is valid. All range readers pass the quoted recovery floor.
- Test cases live in `testdata/*.yaml` corpora with required-name manifests,
  not inline tables.

## Unstring guard

`unstring/` is the static guard against stringly-typed values: one Go module
holding the rules, the catalog generator and the ratchet checker. Consumers
run it with `go run github.com/peasant-labs/infra/unstring/cmd/unstring@vX.Y.Z`
at a version pinned in one place (their `Makefile` or `package.json`).

### Invariants

- ast-grep is exactly the version in `astgrep.RequiredVersion`; the tool refuses
  any other. Moving it is a MINOR release, together with the flake and CI pins
  of every consumer.
- No command raises a baseline count after first adoption. `init` runs only
  when no baseline exists; `update` only lowers, removes and moves entries.
- The two closed-set rules accept no `unstring-allow` marker, and an `exempt`
  entry for them must match generated files only. "This value is one of ours"
  is not a reason on any rule.
- The guard never edits a consumer's own ast-grep config and never passes
  `--error=unused-suppression`.
- Tests are fixtures with required-name manifests and run the real ast-grep;
  without it they fail with the install command, never skip.

### Versioning

A change to what a rule matches, to the catalog sources, to deduplication, to
the rule set or to the required ast-grep version is MINOR, and its release note
lists each change, whether it can add or remove hits, and how many lines it
matched on the three consumers' default branches. Message and fix text that
changes no count is PATCH.

### Release

The release automation and operator setup are documented in
`unstring/README.md`. Include per-rule changes and measured line counts in
the change PR; the generated release notes link to it.

Each consumer bumps its pin in one pull request: run the new version's
`check` to preview, fix (or, where the rule accepts one, mark) every new hit,
change the pin, run the update command, and commit the pin, the fixes and
the baseline together.

## Validation

- `make check` for anything under `stacks/` or `modules/`.
- `make release-tool-check` for anything under `tools/release/`.
- `actionlint` for any workflow change.
- For `unstring/`: `gofmt -l .`, `go vet ./...` and `go test ./...` from
  `unstring/`, with ast-grep 0.51.2 on PATH or named by `UNSTRING_AST_GREP`.
- For an image change, actually build the Containerfile and assert the tool you
  added is present in the result. A pin that resolves in the apt index is not the
  same as a working image.
