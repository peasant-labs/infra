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
not list:

- **The default token never writes refs.** Every workflow and every job
  declares `permissions:`. Every scope is `read` or `none`, except
  `id-token: write` (OIDC publishing) anywhere, and `contents: write` only in
  the workflow that runs on nothing but a pushed release tag. Every
  `actions/checkout` sets `persist-credentials: false`.
- **Credentials stay in the listed jobs.** A credential is the App token that
  `actions/create-github-app-token` mints (and every output of that step), the
  secrets a mint step reads, and the default token where it may write. Only the
  policy's release job, its bot jobs and the tag-triggered publish jobs may
  mint or read one, and there only in the mint step's inputs and in the
  step-level `env:` of a run step, under `GH_TOKEN`, `GITHUB_TOKEN` or
  `APP_TOKEN`. Never in a workflow or job `env:`, a job output, a reusable
  workflow, an action's `with:`, or a run block's text.
- **Those jobs use only pinned, allowed actions:** `actions/checkout`,
  `actions/setup-go`, `actions/setup-node`, `actions/create-github-app-token`,
  `actions/upload-artifact`, `pnpm/action-setup` and
  `cachix/install-nix-action`, each at a 40-hex commit. No reusable workflow
  and no `container:` there.
- **A step that receives a credential runs exactly one command** from a
  closed template set, on the job's own bot branch from the release config:

  | template | command |
  |---|---|
  | `push-own-branch` | `git [-c http.extraheader="AUTHORIZATION: bearer ${APP_TOKEN}"] push [--force-with-lease\|--force] [--no-verify] origin <own bot branch \| HEAD:<own bot branch>>` |
  | `push-release-tag` (release job) | `git [-c http.extraheader=...] push origin "[<candidate>:]refs/tags/<tag>"`, both read from the planner's outputs |
  | `pr-create` | `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]` |
  | `pr-edit`, `pr-close` | `gh pr edit\|close <own PR> [--title\|--body\|--comment <text>]` |
  | `pr-merge` | `gh pr merge (--auto [--squash\|--merge\|--rebase] \| --disable-auto) <own PR>`; in the release job only in the arm and hold steps |
  | `merge-queue-read` | `gh api graphql [-F name=literal]... -f query='<a mergeQueue query>' [> file]` |
  | `release-view`, `release-edit`, `release-create` (publish jobs) | `gh release view\|edit\|create <triggering tag> [...]`, and `create` passes `--verify-tag` |

  `<own PR>` is the own bot branch, a literal number, or `"${PR_NUMBER}"`
  mapped from one step output. `<text>` is literal, or double-quoted env
  names that carry no credential such as `"${TITLE}"` (never `${{ }}`).
  The step's env holds only `GH_TOKEN`, `GITHUB_TOKEN`, `APP_TOKEN`, `TAG`, `CANDIDATE`, `PR_NUMBER`,
  `TITLE`, `BODY`, `PRERELEASE` and `LATEST`, and the step runs under the
  default shell. A second line, a separator, a pipe, a substitution, a
  here-document, an interpreter or a repository script is refused.

Do every other piece of work (building the tool, planning, committing,
tagging locally, listing PRs with the read-only default token) in a step that
holds no credential. Such a step may run any command: with no persisted
checkout token and a read-only default token, it has nothing to write a ref
with. Bot branches sit under `automation/`, which the release config enforces,
so no bot branch can name the default branch.
