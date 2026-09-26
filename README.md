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

The organisation runs a self-hosted pool of container runners on a desktop
machine. Repositories call `runner-router.yml` and route with its output, so a
run uses the pool when it is online and a hosted fallback when it is not — no
run ever queues on a machine that is switched off.

```yaml
jobs:
  determine-runner:
    uses: peasant-labs/infra/.github/workflows/runner-router.yml@runner-router-v1
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
