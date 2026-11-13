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
                                          gh pr create | edit | merge | close
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
  plumbing template or one credential template. No service container, no
  `working-directory`, and every step's env (workflow, job and step) holds only
  `GH_TOKEN`, `GITHUB_TOKEN`, `APP_TOKEN`, `TAG`, `PR_NUMBER`, `TITLE`, `BODY`,
  `PRERELEASE` and `LATEST`. Every `needs.<job>` it reads is in its `needs:`,
  and every artifact it downloads is uploaded by a job in its `needs:`.
- **`credential-steps-run-one-template`.** A step that receives a credential
  runs exactly one credential template, on the job's own bot branch from the
  release config.

| plumbing template (no credential) | command |
|---|---|
| `fetch-bundle-branch` | `git fetch [--no-tags] <file>.bundle <own bot branch>:<own bot branch>` |
| `fetch-bundle-tag` (release job) | `git fetch [--no-tags] <file>.bundle "refs/tags/${TAG}:refs/tags/${TAG}"` |

| credential template | command |
|---|---|
| `push-own-branch` | `git [-c http.extraheader="AUTHORIZATION: bearer ${APP_TOKEN}"] push [--force-with-lease\|--force] [--no-verify] origin <own bot branch \| HEAD:<own bot branch>>` |
| `push-release-tag` (release job) | `git [-c http.extraheader=...] push origin "refs/tags/${TAG}:refs/tags/${TAG}"` |
| `pr-create` | `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]` |
| `pr-edit`, `pr-close` | `gh pr edit\|close [--title\|--body\|--comment <text>] <own PR>` |
| `pr-merge` | `gh pr merge (--auto [--squash\|--merge\|--rebase] \| --disable-auto) <own PR>`; in the release job only in the arm and hold steps |
| `merge-queue-read` | `gh api graphql [-F name=literal]... -f query='<a mergeQueue query>' [> file]` |
| `release-view`, `release-edit`, `release-create` (publish jobs) | `gh release view\|edit\|create <triggering tag> [...]`, and `create` passes `--verify-tag` |

The variable parts are typed:

- `TAG` is mapped from the planner's `tag` output: `steps.<plan>.outputs.tag`,
  or a `needs.<job>.outputs.<name>` that job maps from it. In the publish
  workflow it is mapped from `${{ github.ref_name }}`, or the command reads
  `"$GITHUB_REF_NAME"`. The tag refspec is written on both sides: git splits a
  refspec at its last `:`, so a `:` in `TAG` leaves a source that names no ref
  and the push fails, instead of retargeting it.
- `<own PR>` is the own bot branch, a literal number, or `-- "${PR_NUMBER}"`,
  mapped from one step output or one output of a needed job. The `--` keeps gh
  from reading a value that starts with `-` as a flag.
- `<text>` is literal, or double-quoted env names that carry no credential,
  such as `"${TITLE}"`.

No run text in such a job holds a `${{ }}` expression: an expression is
pasted into the script before the shell runs, so a value from the planning job
could end a quote and add a command. A second line, a separator, a pipe, a
substitution, a here-document, an interpreter or a repository script is
refused.

Bot branches sit under `automation/`, which the release config enforces, so no
bot branch can name the default branch.
