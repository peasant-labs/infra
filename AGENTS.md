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

## Validation

- `make check` for anything under `stacks/` or `modules/`.
- `actionlint` for any workflow change.
- For an image change, actually build the Containerfile and assert the tool you
  added is present in the result. A pin that resolves in the apt index is not the
  same as a working image.
