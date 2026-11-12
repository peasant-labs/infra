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
