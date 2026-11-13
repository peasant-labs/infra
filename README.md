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

Run its checks with `make release-tool-check` (gofmt, build, vet, test). The
`Release tool` workflow runs the same steps on every pull request.

### What a consumer workflow may do with a credential

`release-intent check-workflows` does not try to read every way a shell can
write a ref. It confines the credentials instead, and rejects anything it does
not list.

**Every release, bot and publish job is two jobs.** A job without a
credential does the work. A job with the credential only pushes or calls
`gh`, and takes everything it pushes from the first job:

```
plan (no credential)                    release (credential), needs: [plan]
  build release-intent, plan, verify      checkout          persist-credentials: false
  release-pr --write, commit              download-artifact name: release-bundles, path: bundles
  git tag -a <tag>                        create-github-app-token
  git bundle create bundles/*.bundle      git fetch bundles/tag.bundle "refs/tags/${TAG}:refs/tags/${TAG}"
  read the merge queue, queue-gate        git -c http.extraheader=... push origin "refs/tags/${TAG}:refs/tags/${TAG}"
  upload-artifact release-bundles         git fetch bundles/release.bundle automation/release:automation/release
  outputs: tag, action, push, gate, ...   git -c http.extraheader=... push --force origin automation/release
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
  secrets a mint step reads, and the default token where it may write. Only the
  policy's release job, its bot jobs and the tag-triggered publish jobs may
  mint or read one, and there only in the mint step's inputs and in the
  step-level `env:` of a run step, under `GH_TOKEN`, `GITHUB_TOKEN` or
  `APP_TOKEN`.
- **`credential-jobs-use-pinned-actions`.** A job that may hold a credential
  uses only `actions/checkout` (`persist-credentials`, `fetch-depth`,
  `fetch-tags`), `actions/download-artifact` (`name`, and `path` set to a
  literal subdirectory) and `actions/create-github-app-token` (`app-id`,
  `client-id`, `private-key`, `owner`, `repositories`, `permission-contents`,
  `permission-pull-requests`), each pinned to a 40-hex commit. No setup action,
  no upload, no reusable workflow, no container.
- **`credential-jobs-run-no-general-steps`.** Every run step of that job is one
  plumbing template or one credential template. The job runs on one literal
  GitHub-hosted label (`ubuntu-latest`, `ubuntu-24.04`, `ubuntu-22.04`), never
  an expression (a runner-router output included), `self-hosted`, a label list
  or a group: a hosted runner is a fresh machine per job. No service container,
  no `working-directory`, and every step's env (workflow, job and step) holds
  only `GH_TOKEN`, `GITHUB_TOKEN`, `APP_TOKEN`, `TAG`, `TITLE`, `BODY`,
  `PRERELEASE` and `LATEST`. Every `needs.<job>` it reads is in its `needs:`,
  and every artifact it downloads is uploaded by a job in its `needs:`.
- **`credential-steps-run-one-template`.** A step that receives a credential
  runs exactly one credential template, on the job's own bot branch from the
  release config.
- **`tag-publish-verifies-the-tag`.** In every workflow that runs on pushed
  tags, every job that can publish (`contents: write` or `id-token: write`)
  needs a **tag-verify job**, and its `if` carries no `always()`, `cancelled()`
  or `failure()`. `verify-tag` exits 0 only when the tag is the release prefix
  plus an X.Y.Z version and the default branch reaches its commit, reading the
  release config from that branch. A tag push needs only a write to
  `refs/tags/`, so nothing publishes a tag the tool has not checked.

  The tag-verify job's success is the verdict on the tag, so it is confined
  the way a credential job is. It holds no credential and no write, runs on
  one literal GitHub-hosted label, and runs only these steps:

  | step | shape |
  |---|---|
  | `repo-checkout` | `actions/checkout@<40-hex>` with `persist-credentials: false` and optionally `fetch-depth`, `fetch-tags` |
  | `tool-checkout` | `actions/checkout@<40-hex>` with `repository: peasant-labs/infra`, `ref: <40-hex>`, `path: <dir>`, `persist-credentials: false`, and optionally `sparse-checkout` |
  | `setup-go` | `actions/setup-go@<40-hex>` with `go-version-file: <dir>/tools/release/go.mod` and `cache: false` |
  | `build` | `go build -C <dir>/tools/release -o "${RUNNER_TEMP}/release-intent" ./cmd/release-intent`, with `GOWORK: "off"` |
  | `verify` | `"${RUNNER_TEMP}/release-intent" verify-tag --tag "${GITHUB_REF_NAME}" --branch "origin/${DEFAULT_BRANCH}"` (`--branch` may also be `"${DEFAULT_BRANCH}"` or `"refs/remotes/origin/${DEFAULT_BRANCH}"`) |

  Every input is a literal. The env (workflow, job and step) holds only
  `DEFAULT_BRANCH: ${{ github.event.repository.default_branch }}` and
  `GOWORK: "off"`. No step sets `shell`, `working-directory`, `if` or
  `continue-on-error`, and the job sets no `defaults`, `container`,
  `services`, `strategy` or `continue-on-error`. The workflow sets no
  `defaults` either. Each run step is exactly one command, read by the same
  reader as a credential step, so the step exits with the command's status.

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

| plumbing template (no credential) | command |
|---|---|
| `fetch-bundle-branch` | `git fetch [--no-tags] <file>.bundle <own bot branch>:<own bot branch>` |
| `fetch-bundle-tag` (release job) | `git fetch [--no-tags] <file>.bundle "refs/tags/${TAG}:refs/tags/${TAG}"` |

| credential template | command |
|---|---|
| `push-own-branch` | `git [-c http.extraheader="AUTHORIZATION: bearer ${APP_TOKEN}"] push [--force-with-lease\|--force] [--no-verify] origin <own bot branch \| HEAD:<own bot branch>>` |
| `push-release-tag` (release job) | `git [-c http.extraheader=...] push origin "refs/tags/${TAG}:refs/tags/${TAG}"` |
| `pr-create` | `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]` |
| `pr-edit`, `pr-close` | `gh pr edit\|close [--title\|--body\|--comment <text>] [--] <own bot branch>` |
| `pr-merge` | `gh pr merge (--auto [--squash\|--merge\|--rebase] \| --disable-auto) [--] <own bot branch>`; in the release job only in the arm and hold steps |
| `merge-queue-read` | `gh api graphql [-F name=literal]... -f query='<a mergeQueue query>' [> file]` |
| `release-view`, `release-edit`, `release-create` (publish jobs) | `gh release view\|edit\|create <triggering tag> [...]`, and `create` passes `--verify-tag` |

The variable parts are typed:

- `TAG` is mapped from the planner's `tag` output: `steps.<plan>.outputs.tag`,
  or a `needs.<job>.outputs.<name>` that job maps from it. In the publish
  workflow it is mapped from `${{ github.ref_name }}`, or the command reads
  `"$GITHUB_REF_NAME"`. The tag refspec is written on both sides: git splits a
  refspec at its last `:`, so a `:` in `TAG` leaves a source that names no ref
  and the push fails, instead of retargeting it.
- A PR is named by the job's own bot branch, never by a number: a number from
  the planning job's outputs could name any PR, and the App token would then
  retitle, arm, disarm or close it.
- `<text>` is literal, or double-quoted env names that carry no credential,
  such as `"${TITLE}"`.

No run text in such a job holds a `${{ }}` expression: an expression is
pasted into the script before the shell runs, so a value from the planning job
could end a quote and add a command. A second line, a separator, a pipe, a
substitution, a here-document, an interpreter or a repository script is
refused.

Bot branches sit under `automation/`, which the release config enforces, so no
bot branch can name the default branch.

**What the bot PRs may contain.** The release and cut-release branches are
built by the job without a credential and land unreviewed, so `check-title`
judges each bot PR by what that bot writes. The release PR may change only the
changelog and the protected field of each version file; any other path, or
any other change inside a version file, is `release-content-scope`. The
cut-release PR is one empty commit and may change no path. A path in
`protected_paths` is release content for no PR: unlist it in a reviewed config
change before editing it.
