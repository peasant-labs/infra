# infra

Shared infrastructure for Peasant Labs: **cloud control plane**, **reusable CI**,
and **container image maintenance**.

Anything another `peasant-labs` repository calls as a workflow, trusts as a build
input, or deploys through lives here.

## Areas

| Area | Path | What it is |
|---|---|---|
| Cloud control plane | `stacks/`, `modules/` | Terraform roots for `village-production` and `pkgs-production` |
| Reusable CI | `.github/workflows/runner-router.yml` | the single source of truth for routing jobs to the self-hosted runner pool |
| Container images | `runner-image/`, `.github/workflows/runner-image.yml` | the `quay.io/peasant-labs/github-runner` recipe, its publishing, and its keyless signing |

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
