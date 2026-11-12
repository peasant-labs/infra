# infra

Shared infrastructure for Peasant Labs: **cloud control plane**, **reusable CI**,
and **container image maintenance**.

Anything another `peasant-labs` repository calls as a workflow, trusts as a build
input, or deploys through lives here. New shared workflows and organization-wide
automation belong here, not in `peasant-labs/.github`; the Discord pull request
notifier remains in `.github` for now.

## Areas

| Area | Path | What it is |
|---|---|---|
| Cloud control plane | `stacks/`, `modules/terraform/` | Terraform roots for `village-production` and `pkgs-production` |
| Reusable CI | `.github/workflows/runner-router.yml` | the single source of truth for routing jobs to the self-hosted runner pool |
| Container images | `runner-image/`, `.github/workflows/runner-image.yml` | the `quay.io/peasant-labs/github-runner` recipe, its publishing, and its keyless signing |
| Runner pool module | `modules/nixos/`, `flake.nix` | run your own pool by importing `nixosModules.default` |
| Tracker automation | `scripts/tracker_epic_sync.py`, `.github/workflows/tracker-epic-sync.yml` | sweeps organization epics into the Peasant Labs Tracker project |

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

## Tracker automation

`.github/workflows/tracker-epic-sync.yml` sweeps every open epic in the
organization into the [Peasant Labs Tracker](https://github.com/orgs/peasant-labs/projects/4)
project every 15 minutes. It runs on a hosted runner on purpose: it must work
when the self-hosted pool is off, and it does not touch production.

The native "Auto-add to project" workflow cannot cover the organization: GitHub
Free allows one auto-add workflow, each workflow targets one repository, and its
filters cannot match issue titles. The sweeper treats an issue as an epic when it
has the `epic` label, its title starts with `[Epic]` / `Epic` / `EPIC`, or it has
sub-issues. New epics get `Status = Backlog`, a size (`L`, or `XL` above ten
sub-issues), the repository's team, and a priority parsed from a `[P0]`-`[P3]`
title tag or an explicit severity/priority line in the body (P2 otherwise). An
existing Priority set at issue creation is preserved; no existing value is
overwritten. The sweep only fills empty fields, so it also repairs drift.

The workflow needs the repository secret `PROJECT_TOKEN`, a fine-grained PAT
owned by `peasant-labs` with **Issues: read and write** on all repositories and
**Projects: read and write** on the organization. Without it the job fails
closed and writes nothing. Run the workflow manually with `dry_run` enabled to
inspect decisions first.
