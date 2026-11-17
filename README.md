# infra

Shared infrastructure for Peasant Labs: **cloud control plane**, **reusable CI**,
and **container image maintenance**.

Anything another `peasant-labs` repository calls as a workflow, trusts as a build
input, or deploys through lives here.

## Areas

| Area | Path | What it is |
|---|---|---|
| Cloud control plane | `stacks/`, `modules/terraform/` | Terraform roots for `village-production` and `pkgs-production` |
| Reusable CI | `.github/workflows/runner-router.yml` | the single source of truth for routing jobs to the self-hosted runner pool |
| Container images | `runner-image/`, `.github/workflows/runner-image.yml` | the `quay.io/peasant-labs/github-runner` recipe, its publishing, and its keyless signing |
| Runner pool module | `modules/nixos/`, `flake.nix` | run your own pool by importing `nixosModules.default` |
| Release tool | `tools/release/` | the `release-intent` CLI that plans and checks merge-queue releases in every repository |

## Running your own pool

Add `infra` as a flake input and import the module. It configures the podman
options it needs, pulls and verifies the signed image, and runs the containers.
The defaults point at the `peasant-labs` image, so a minimal host needs only a
registration token:

```nix
{
  inputs.infra.url = "github:peasant-labs/infra";

  outputs = { nixpkgs, infra, ... }: {
    nixosConfigurations.desktop = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        infra.nixosModules.default
        ({ config, ... }: {
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

Publishing your own image means overriding `imageRef` **and** `imageSigner`
together; the signer must be the workflow that signed the digest. See `AGENTS.md`.

## The runner pool

The shared router targets the desktop's per-job MicroVM scale set by default.
It reads the dispatcher's health and capacity heartbeat and chooses the caller's
fallback when the pool is unavailable or full. Deployment and caller cutover
must be verified separately; the generic persistent-container module remains
available for other hosts, and a host that retires it keeps the module importable
for rollback (see `docs/runner-pool.md`).

```yaml
jobs:
  determine-runner:
    uses: peasant-labs/infra/.github/workflows/runner-router.yml@<reviewed-40-character-commit-sha>
    with:
      fallback: '["ubuntu-24.04"]'
    secrets:
      RUNNER_STATUS_TOKEN: ${{ secrets.RUNNER_STATUS_TOKEN }}

  my-job:
    needs: determine-runner
    runs-on: ${{ fromJson(needs.determine-runner.outputs.runner) }}
```

Never inline a runner-availability probe. See `AGENTS.md` for the full
invariants and the image trust model.

## The release tool

`tools/release` is its own Go module
(`github.com/peasant-labs/infra/tools/release`). It builds `release-intent`,
the CLI each repository's workflows run to check PR titles, plan the next
version, write release content, and check the repository's workflows against
its merge-queue policy (`.github/release-queue.policy.yml`).

A repository does not import the module. Its workflows check out this
repository at a full commit SHA and build the command from that checkout:

```yaml
- uses: actions/checkout@<sha>
  with:
    repository: peasant-labs/infra
    ref: <40-hex infra commit>
    path: infra-tool
- run: go build -o "$RUNNER_TEMP/release-intent" ./tools/release/cmd/release-intent
  working-directory: infra-tool
```

The [contract and freeze handoff reference](tools/release/README.md) describes
the report, state and plan files and the new commands.

Run its checks with `make release-tool-check` (gofmt, build, vet, test). The
`Release tool` workflow runs the same steps on every pull request.

### Prepare a release branch locally

In a job with **no App credential**, check out the configured release bot
branch at the source branch's tip and run from the repository root:

```sh
release-intent release-pr --branch <source-ref> --commit [--freeze-plan <file>] [--current <existing-release-ref>] [--floor <sha>]
```

`--commit` is an alternative to `--write` and `--check`, not an additional
switch. It requires a clean index and permits unstaged regular files
covered by the source config's `protected_paths`, plus `freeze_outputs` only
when a validated `--freeze-plan` is supplied. Each frozen surface must have a
dirty numbered primary document, as described in the
[freeze handoff reference](tools/release/README.md). It
uses the source commit's config and refuses unsafe or duplicate output paths,
symlinks, multiply linked files, non-regular or missing source files, and an
active Git reference hook.
It stages only the generated changelog, named JSON version-field changes, and
permitted protected and plan-authorized freeze paths,
verifies the staged paths and bytes, then creates one local commit with the fixed
releaser-bot author and committer and the source commit's timestamp. The JSON
bytes outside the named field stay unchanged. The config refuses protected-path
patterns that match the changelog or a version file. The validated source and
exact commit title are printed as
`source=` and `title=`. `push=true` means the checked-out
bot branch now points to that commit; its HEAD can be bundled against the source
commit. `push=false` means the existing `--current` branch already has the
same validated title and fresh, in-scope content and merges
cleanly; no commit is made. Omit `--current` when there is no existing branch.
An unreadable current ref, a required push with no generated changes, or a
version-only result with no changelog change is an error, not a no-op. The
planner identifies release candidates by their changelog change. After an
interrupted or failed staging operation, discard
the incomplete checkout and retry from a clean one. The new mode requires a
Unix runner; existing `--write` remains a working-tree-only operation.

This command does not push, tag, create a PR, or verify permission to publish.
The separately required independent pre-push verifier and its workflow safety
gates must still be implemented and reviewed before enabling the release pilot.
The workflow policy recognizes both `--write` and `--commit` as the mode that
prints the push decision. A consuming workflow still needs its own reviewed
policy and workflow integration before it can use the commit mode.

### What a consumer workflow may do with a credential

`release-intent check-workflows` does not try to read every way a shell can
write a ref. It confines the credentials instead, and rejects anything it does
not list. `--print-required` prints the sorted context names even on a rejected
tree, but still exits with the policy verdict.

**Release, bot and publish work is split between jobs.** A job without a
credential does the work. A job with the credential moves prepared bytes or
calls `gh`, and takes everything it pushes from the first job. A listed comment
job has the explicit reviewed-script exception described below.

The following diagram illustrates credential confinement, not a complete
safe tag-push job graph: the independent pre-push verifier is still required.

```
plan (no credential)                    release (credential), needs: [plan]
  build release-intent, plan, verify      checkout          persist-credentials: false
  release-pr --commit                     download-artifact name: release-bundles, path: bundles
  git tag -a <tag>                        create-github-app-token
  git bundle create bundles/*.bundle      git fetch bundles/tag.bundle "refs/tags/${TAG}:refs/tags/${TAG}"
  read the merge queue, queue-gate        git -c <fixed credential helper> push origin "refs/tags/${TAG}:refs/tags/${TAG}"
  upload-artifact release-bundles         git fetch bundles/release.bundle automation/release:automation/release
  outputs: tag, action, push, gate, ...   git -c <fixed credential helper> push --force origin automation/release
                                          gh pr create | edit | merge | close  -- automation/release
```

The two jobs run on separate runners, so nothing the first job does (a
`$GITHUB_ENV` or `$GITHUB_PATH` entry, a git config or hook, a rewritten
action) can reach the job that holds the credential. The handoff is job
outputs, read into the step env (`TAG: ${{ needs.plan.outputs.tag }}`), and
one artifact holding git bundles.

**The rules:**

- **`default-token-read-only`.** Every workflow and every job declares
  `permissions:`. Every scope is `read` or `none`, except `id-token: write`
  (OIDC publishing) anywhere, and `contents: write` only in the workflow that
  runs on nothing but a pushed release tag. Every `actions/checkout` sets
  `persist-credentials: false`.
- **`credentials-stay-in-listed-jobs`.** A credential is the App token that
  `actions/create-github-app-token` mints (and every output of that step), the
  secrets a mint step reads, the policy-named comment PAT, and the default token
  where it may write. Only the policy's release job, its bot and comment jobs,
  and the tag-triggered publish jobs may
  mint or read one, and there only in the mint step's inputs and in the
  step-level `env:` of a run step, under `GH_TOKEN`, `GITHUB_TOKEN` or
  `APP_TOKEN` or `JOURNEY_APP_TOKEN`. A policy-named PAT is accepted only in
  the listed comment step's own env, not in a mint input. When a PAT is
  listed, workflow YAML must use unique mapping keys and no aliases: those
  forms could otherwise hide a secret from the reader.
- **`credential-jobs-use-pinned-actions`.** A job that may hold a credential
  uses only `actions/checkout` (`persist-credentials`, `fetch-depth`,
  `fetch-tags`), `actions/download-artifact` (`name`, and `path` set to a
  literal subdirectory) and `actions/create-github-app-token` (`app-id`,
  `client-id`, `private-key`, `owner`, `repositories`, `permission-contents`,
  `permission-issues`, `permission-pull-requests`), each pinned to a 40-hex
  commit. A comment checkout may also set `ref` to the default-branch
  expression and `path: reviewed`, and a `workflow_run` comment job's download
  may also set `run-id` to `${{ github.event.workflow_run.id }}` (see below).
  A tag-publish job may additionally use the
  pinned `softprops/action-gh-release` action under the asset constraints
  below. No setup action, no upload, no reusable workflow, no container.
- **`credential-jobs-run-no-general-steps`.** Every run step of that job is one
  plumbing template or one credential template. The job runs on one literal
  GitHub-hosted label (`ubuntu-latest`, `ubuntu-24.04`, `ubuntu-22.04`), never
  an expression (a runner-router output included), `self-hosted`, a label list
  or a group: a hosted runner is a fresh machine per job. No service container,
  no `working-directory`, and every step's env (workflow, job and step) holds
  only `GH_TOKEN`, `GITHUB_TOKEN`, `APP_TOKEN`, `JOURNEY_APP_TOKEN`, `TAG`,
  `TITLE`, `BODY`, `PRERELEASE`, `LATEST`, `PR_NUMBER` and `EVIDENCE_DIR`.
  `PR_NUMBER`, `EVIDENCE_DIR` and `JOURNEY_APP_TOKEN` are comment-job-only.
  Every `needs.<job>` it reads is in its `needs:`,
  and every artifact it downloads is uploaded by a job in its `needs:`.
- **`credential-steps-run-one-template`.** A step that receives a credential
  runs exactly one credential template, on the job's own bot branch from the
  release config, the triggering tag, or the triggering PR as appropriate.
  A comment job and its comment steps must not set `continue-on-error`.
- **`tag-publish-verifies-the-tag`.** In every workflow that runs on pushed
  tags, every job that can publish (`contents: write` or `id-token: write`)
  needs a **tag-verify job**, and its `if` calls no status function
  (`success()`, `failure()`, `always()` or `cancelled()`, in any case and with
  any spacing before the `(`) and reads no `needs.<job>.result`. GitHub adds
  its implicit `success()` check only when the expression calls no status
  function, so those are the spellings that would run the publish job after a
  failed or skipped verify job. `verify-tag` exits 0 only when the tag is the
  release prefix plus an X.Y.Z version and the default branch reaches its
  commit, reading the release config from that branch. A tag push needs only a
  write to `refs/tags/`, so nothing publishes a tag the tool has not checked.

  An unfiltered `push` includes tag pushes, whether written as a scalar, in an
  event list, with a null or empty mapping, or with only path filters (GitHub
  does not apply path filters to tag pushes). Explicit `tags` or `tags-ignore`
  filters, including alongside branch filters, are tag-capable. A push with
  only branch filters is not. The checker refuses malformed or ambiguous
  publisher triggers rather than assuming they exclude tags. This does not
  broaden the separate `contents: write` exception: that still requires the
  single, explicit release-tag-only trigger.

  The tag-verify job's success is the verdict on the tag, so it is confined
  the way a credential job is. It holds no credential and no write, runs on
  one literal GitHub-hosted label, carries no job-level `if` (a skipped verify
  job must skip every job that needs it), and runs only these steps:

  | step | shape |
  |---|---|
  | `repo-checkout` | `actions/checkout@<40-hex>` with `persist-credentials: false` and optionally `fetch-depth`, `fetch-tags` |
  | `tool-checkout` | `actions/checkout@<40-hex>` with `repository: peasant-labs/infra`, `ref: <40-hex>`, `path: <dir>`, `persist-credentials: false`, and optionally `sparse-checkout` |
  | `setup-go` | `actions/setup-go@<40-hex>` with `go-version-file: <dir>/tools/release/go.mod` and `cache: false` |
  | `build` | `go build -C <dir>/tools/release -o "${RUNNER_TEMP}/release-intent" ./cmd/release-intent`, with `GOWORK: "off"` |
  | `verify` | `"${RUNNER_TEMP}/release-intent" verify-tag --tag "${GITHUB_REF_NAME}" --branch "origin/${DEFAULT_BRANCH}"` (`--branch` may also be `"${DEFAULT_BRANCH}"` or `"refs/remotes/origin/${DEFAULT_BRANCH}"`) |

  Every input is a literal. The env (workflow, job and step) holds only
  `DEFAULT_BRANCH: ${{ github.event.repository.default_branch }}` and
  `GOWORK: "off"`. No step sets `shell`, `working-directory`, `if` or the
  `continue-on-error` key, and the job sets no `defaults`, `container`,
  `services`, `strategy` or the `continue-on-error` key. The workflow sets no
  `defaults` either. Each run step is exactly one command, read by the same
  reader as a credential step, so the step exits with the command's status.

  The policy refuses the `continue-on-error` **key**, not a spelling of its
  value: GitHub evaluates the key as a value, and the documented
  `continue-on-error: ${{ fromJSON('true') }}` form makes it true at run time.
  A literal-only comparison would be a bypass, so an absent key is the only
  shape the checker accepts wherever this or another rule says
  "no `continue-on-error`".

  Each limit closes one way the job could succeed without the tool's yes:
  - The single-command reading refuses `|| true`, `; exit 0`, `| cat`, a
    second line, `set +e`, `& wait` and a custom shell such as `bash {0}`.
  - The tool runs by its absolute path, and the env refuses `PATH`, so neither
    a `PATH` entry nor a stub on `PATH` can answer in its place.
  - `GOWORK=off` stops a `go.work` file in the tag's checkout, which sits in a
    parent directory of the tool, from replacing the tool's modules.
  - `cache: false` keeps setup-go from restoring a build cache written by
    another run.
  - The env refuses `BASH_ENV`, `GOFLAGS`, `GOTOOLCHAIN` and `GOPROXY`, each
    of which can change what runs or what the build compiles.

  The pre-tag checks of `release-actions-gated` are read through the same
  single-command reader.

- **`release-tag-checked-before-push`.** The release job is the only job that
  can write a tag, and a tag-triggered workflow runs the workflow files of the
  tagged commit, so the tag has to be checked before it leaves that job.
  - The release workflow triggers only on `push`, `schedule` and
    `workflow_dispatch`: never on a pushed tag, a `workflow_run`, or another
    event without its own proof for a release job. Its `push` must therefore
    have branch filters and no tag filters; an unfiltered `push` includes tags.
  - The release job's `if` proves the run is on the default branch: the
    comparison `github.ref == format('refs/heads/{0}', github.event.repository.default_branch)`
    is one conjunct of a conjunction. A disjunction, a negation, a second
    comparison, or a literal branch name is refused: the checker cannot prove
    a literal names the default branch, and a run on another branch executes
    that branch's copy of the workflow.
  - After fetching the tag from its bundle and before pushing it, a
    `check-tag-ancestor` step runs `git merge-base --is-ancestor
    "refs/tags/${TAG}" "${GITHUB_SHA}"` under the tag push's own `if`. The
    check and the push use the same `if`, and that condition may compare only
    a completed planning job's outputs (for example
    `needs.plan.outputs.tag-action == 'tag'`). It may not read `steps`, `env`,
    `job` or a function: GitHub evaluates each step's `if` at that step's own
    time, so `steps.check-tag.outcome` is absent while the check is evaluated
    and `skipped` while the push is evaluated. That is how the same text can
    skip the check and then run the push; the condition is therefore
    time-invariant, and the push's implicit `success()` check is what makes
    it run only after the check succeeded. Neither the check step, the tag
    push, nor the release job may set the `continue-on-error` key at any
    value: GitHub evaluates the key as a value, so an expression such as
    `${{ fromJSON('true') }}` would make a failed check report success. The
    tag push's `if` calls no status function either, so a failed check skips
    the push through GitHub's implicit `success()` check instead of being
    overridden by `always()`, `failure()`, `cancelled()` or `success()`.

  For a `push`, `github.sha` is the pushed branch's tip; for a `schedule`, the
  latest commit on the default branch; for a `workflow_dispatch`, the commit
  of the selected ref. With the job `if` above, each is a default-branch
  commit, so the check means the tag names a commit that is already on the
  default branch, or is that commit. A run that executes another branch's copy
  of the workflow is outside this checked tree: the copy is unreviewed, and no
  check of the default branch can see it. The release job's repository
  checkout must keep full history (`fetch-depth: 0`): the check reads the
  tag's local ancestry, and a shallow checkout fails closed instead of proving
  anything.

- **Every no-`continue-on-error` clause refuses the key, not a value.** The
  title-guard job and every step of it, every step `release-actions-gated`
  gates, the tag-verify job, the tag ancestry check, the tag push and the
  release job all refuse the `continue-on-error` key whatever its value:
  GitHub evaluates the key as a value, and the documented
  `${{ fromJSON('true') }}` form makes a failed check or job report success.
  Drop the key; a literal `false` is refused too, because the checker cannot
  tell a literal from an expression it cannot evaluate.

**Release-tag ruleset (maintainer step).** A repository that turns tag mode on
must restrict release-tag creation to the releaser App with a repository
ruleset targeting the release tag prefix, whose only bypass actor is that App.
That includes fairtrade, whose `npm-publish.yml` trusts every tag under its
prefix. The checker cannot read repository settings, so this is a recorded
operator step per repository; it does not replace any check above.

| plumbing template (no credential) | command |
|---|---|
| `fetch-bundle-branch` | `git fetch [--no-tags] <file>.bundle <own bot branch>:<own bot branch>` |
| `fetch-bundle-tag` (release job) | `git fetch [--no-tags] <file>.bundle "refs/tags/${TAG}:refs/tags/${TAG}"` |
| `check-tag-ancestor` (release job) | `git merge-base --is-ancestor "refs/tags/${TAG}" "${GITHUB_SHA}"` |

| credential template | command |
|---|---|
| `push-own-branch` | `git [-c <fixed credential helper>] push [--force-with-lease\|--force] [--no-verify] origin <own bot branch \| HEAD:<own bot branch>>` |
| `push-release-tag` (release job) | `git [-c <fixed credential helper>] push origin "refs/tags/${TAG}:refs/tags/${TAG}"` |
| `pr-create` | `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]` |
| `pr-edit`, `pr-close` | `gh pr edit\|close [--title\|--body\|--comment <text>] [--] <own bot branch>` |
| `pr-merge` | `gh pr merge (--auto [--squash\|--merge\|--rebase] \| --disable-auto) [--] <own bot branch>`; in the release job only in the arm and hold steps |
| `merge-queue-read` | `gh api graphql [-F name=literal]... -f query='<a mergeQueue query>' [> file]` |
| `release-view`, `release-edit`, `release-create` (publish jobs) | `gh release view\|edit\|create <triggering tag> [...]`, and `create` passes `--verify-tag` |
| `pr-comment` (`pull_request` comment job only) | `gh pr comment "$PR_NUMBER" [--repo "$GITHUB_REPOSITORY"] [--edit-last] [--create-if-none] (--body-file\|-F) <file>` |
| `run-reviewed-script` (comment job, both modes) | `node reviewed/<policy reviewed_script>` |

The variable parts are typed:

- For GitHub App installation tokens, `<fixed credential helper>` is exactly
  `credential.helper='!f() { echo "username=x-access-token"; echo "password=${APP_TOKEN}"; }; f'`,
  with `APP_TOKEN` mapped to the minted token in the step env. The single
  quotes keep the token out of Git's argument vector and persistent config;
  Git invokes the helper for its HTTP Basic credentials. The `-c` option is
  optional, but when present only this helper is accepted: a bearer
  `http.extraheader` is not a Git push template.
- `TAG` is mapped from the planner's `tag` output: `steps.<plan>.outputs.tag`,
  or a `needs.<job>.outputs.<name>` that job maps from it. In the publish
  workflow it is mapped from `${{ github.ref_name }}`, or the command reads
  `"$GITHUB_REF_NAME"`. The tag refspec is written on both sides: git splits a
  refspec at its last `:`, so a `:` in `TAG` leaves a source that names no ref
  and the push fails, instead of retargeting it.
- A bot PR is named by the job's own bot branch, never by a number: a number from
  the planning job's outputs could name any PR, and the App token would then
  retitle, arm, disarm or close it. A comment job instead maps `PR_NUMBER`
  exactly from `${{ github.event.pull_request.number }}`.
- `<text>` is literal, or double-quoted env names that carry no credential,
  such as `"${TITLE}"`.

No run text in such a job holds a `${{ }}` expression: an expression is
pasted into the script before the shell runs, so a value from the planning job
could end a quote and add a command. A second line, a separator, a pipe, a
substitution or a here-document is refused. An interpreter or a repository
script is refused except for the reviewed-script template below. The helper's
fixed single-quoted literal is the sole permitted shell-valued Git config, not
permission to run general shell code.

**Comment jobs and inline evidence.** A policy can list a comment job without
a bot branch:

```yaml
comment_jobs:
  - workflow: journey.yml
    job: evidence
    reviewed_script: frontend/scripts/journey/ci-post-evidence.mjs
    pat_secret: JOURNEY_GITHUB_USER_PAT
```

`reviewed_script` and `pat_secret` are required for `run-reviewed-script`;
omit them for a job using only `pr-comment`. The mint is gated on the
`pull_request` event, as is the comment step, and explicitly passes one literal repository,
`permission-contents: read`, `permission-issues: write` and
`permission-pull-requests: write`. The default token remains read-only. The
script job checks out the current repository with `ref: ${{
github.event.repository.default_branch }}`, `path: reviewed` and
`persist-credentials: false`. It downloads an artifact from a needed job into
a literal directory disjoint from the entire checkout, passes that directory
as `EVIDENCE_DIR`, and runs exactly the listed script with the PAT and App token
only in its step env. Checkout and the matching download are unconditional,
precede the script and cannot mask errors. No general step is allowed.

This exception accepts a **personal user PAT** to preserve inline media. The
checker cannot bound the PAT's grants or inspect what the reviewed script
does. A same-repository PR can change its own workflow, and the script reads
PR-controlled evidence and paths. The default-branch checkout and separate
artifact directory protect the script and its imports from artifact overwrite;
they do not make hostile evidence safe. Protect the default branch, review
the script and its dependencies and constrain file paths in the script. Fork
PRs normally receive no secrets. Operators accept this residual trust; it is
not equivalent to a single `gh` command.

**Comment jobs for fork and Dependabot PRs: the `workflow_run` mode.** A
`pull_request` run from a fork or from Dependabot gets no secrets, so the
modes above cannot comment there. A comment job whose workflow triggers on
`workflow_run` runs in the base repository with secrets, downloads the
producing run's artifact, and comments. The checker reads the mode from the
workflow's trigger, so a `pull_request` comment job keeps every rule above
unchanged. A `workflow_run` comment job is listed in the policy with both
`reviewed_script` and `pat_secret`:

```yaml
# .github/workflows/apidiff-comment.yml
on:
  workflow_run:
    workflows: [Tests]
    types: [completed]
permissions:
  contents: read
jobs:
  comment:
    if: github.event.workflow_run.event == 'pull_request'
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      actions: read
    steps:
      - uses: actions/checkout@<40-hex>
        with:
          ref: ${{ github.event.repository.default_branch }}
          path: reviewed
          persist-credentials: false
      - uses: actions/download-artifact@<40-hex>
        with:
          name: go-apidiff-comment
          run-id: ${{ github.event.workflow_run.id }}
          path: evidence
      - env:
          GH_TOKEN: ${{ secrets.APIDIFF_COMMENT_PAT }}
          EVIDENCE_DIR: evidence
        run: node reviewed/scripts/ci-post-apidiff-comment.mjs
```

The checker accepts the mode only when all of these hold, each under
`credential-steps-run-one-template` unless noted:

- **Trigger.** `on:` is `workflow_run` alone, with exactly one literal producing
  workflow in `workflows:`, `types: [completed]`, and no other key. A second
  trigger of any kind is refused, and so is `pull_request_target` in any
  comment job's workflow. `pull_request_target` stays refused: it runs with
  secrets on a PR's behalf and is not a member of the event set.
- **Gate.** The job's own `if` carries `github.event.workflow_run.event ==
  'pull_request'` as one conjunct, with no disjunction. The `pull_request`
  comparison is not a gate here: `github.event_name` is `workflow_run`.
  Further conjuncts (for example `github.event.workflow_run.conclusion ==
  'success'`) are accepted.
- **No PR code.** The job's only checkout is the default branch (`ref: ${{
  github.event.repository.default_branch }}`, `path: reviewed`,
  `persist-credentials: false`), unconditional and before the script. There is
  no other checkout, no `repository:` input and no setup action. The checker
  never reads the PR head, so the script runs reviewed code only.
- **Evidence.** One unconditional download before the script, from the run
  that triggered this one (`run-id: ${{ github.event.workflow_run.id }}`, the
  only `run-id` accepted, and no `github-token` or `repository` input), into a
  literal directory that is `EVIDENCE_DIR` and does not overlap `reviewed`.
  The job grants `contents: read` and `actions: read` and no write.
- **Credential.** The policy PAT is the only credential: in the env of the one
  reviewed-script step, never in a general step, workflow or job env, an
  output, an action input or run text (`credentials-stay-in-listed-jobs`).
  The job mints no App token (`credentials-stay-in-listed-jobs`), and
  `JOURNEY_APP_TOKEN` and `PR_NUMBER` are refused, because the event carries
  no pull request and the number comes from the artifact.
- **Template.** The only credential step is `run-reviewed-script`, exactly
  once. `gh pr comment` is not available: its target would be a number read
  from PR-controlled evidence, and only the reviewed script can validate it.
  Any other step is a general step (`credential-jobs-run-no-general-steps`).
- **Failure.** No `continue-on-error` on the job or any comment step, and the
  script step's `if` calls no status function.
- **Workflow.** Every job of the workflow is a listed comment job: an
  unreviewed job in this workflow would run in the base repository with
  secrets and the PR author's artifacts in reach.

**Missing artifact.** The comment job fails closed. A missing artifact makes
the download fail and the job red. A clean no-op would need either
`continue-on-error` on the download (refused, because it also masks a failed
download that should be an error) or a conditional download, and the checker
accepts neither. A credential-free probe job whose output gates the comment
job is not modelled either, because the workflow may hold no other job. A
producer that wants a quiet run should upload the artifact on every
`pull_request` run (an empty report is a valid report) and filter in the
job's `if` with a further conjunct.

**What the checker cannot see in this mode.** The PR number comes from the
artifact, which a fork PR author controls, and the PAT is a user token. The
script must treat the number and every file as hostile: accept a positive
integer only, read the event payload at `GITHUB_EVENT_PATH` and refuse any PR
whose head commit is not `workflow_run.head_sha`, and constrain every path it
reads. The checker pins who runs the script, not what the script does, which
is the same accepted exception as the `pull_request` reviewed script.

**Prepared Release assets.** A tag-only publish job may use the pinned
release-assets action with `tag_name: ${{ github.ref_name }}`,
`repository: ${{ github.repository }}`, `files: dist/*`,
`fail_on_unmatched_files: true` and `overwrite_files: true`. `prerelease` and
`make_latest` read outputs of needed jobs. The job needs the credential-free
`build` and tag verifier, and downloads the sole `release-dist` upload from
`build`'s `dist` into `dist`. Earlier typed `release-view` and
`release-create --verify-tag` steps for this repository gate the action on
either step's success. The glob is action input data, not shell syntax.
The publisher has one unconditional download; every upload-artifact name in
the workflow is literal so a different job cannot alias `release-dist`.
Neither the publish job nor the assets action may mask a failed upload with
`continue-on-error`.
The build verifies `checksums.txt` before uploading it with the assets; this
is a consumer workflow obligation, not a cryptographic signature. A compromised
build can still provide malicious bytes: this split confines credentials, not
the trustworthiness of the build.

Bot branches sit under `automation/`, which the release config enforces, so no
bot branch can name the default branch.

**What the bot PRs may contain.** The release and cut-release branches are
built by the job without a credential and land unreviewed, so `check-title`
judges each bot PR by what that bot writes. The release PR may change the
changelog, `protected_paths`, `freeze_outputs`, and the protected field of each
version file. A version file may change only in its protected field; config
patterns that match version files or the changelog are refused. Other paths
are `release-content-scope`. The title guard
cannot see the freeze plan; commit preparation requires it before carrying
`freeze_outputs`. The cut-release PR is one empty commit and may change no
path. A `protected_paths` entry is forbidden to content PRs; `freeze_outputs`
alone does not protect a path.
