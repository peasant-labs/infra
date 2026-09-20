# Self-hosted runner pool

Organization CI runs on a self-hosted pool with hosted runners as a fallback.
This document covers the pool, the shared router in this repository, the pool
image, and the Quay namespace that serves the images CI pulls.

Operational detail for the host itself (the NixOS module, state layout, and
resource slices) lives in the maintainer's dotfiles repository,
`docs/github-runner.md`.

## Shape

- **Pool**: four rootless-podman containers on the maintainer's NixOS desktop,
  labels `self-hosted, linux, x64, container`, runner group `minttea--desktop`,
  registered to the `peasant-labs` organization. Each container runs one job at
  a time; job containers and service containers run against the host podman
  socket.
- **Fallback**: every caller passes its own fallback label set (Blacksmith for
  the release jobs, hosted Ubuntu elsewhere). When no pool runner is online the
  router returns the fallback, so CI never blocks on the desktop.
- **Contract exceptions**: the full-stack e2e job stays on amd64 Blacksmith by
  contract; `fairtrade-design-system` and this repository stay inline because
  their action allow-lists exclude the shared workflow.

## Routing

Every routed repository calls the shared reusable workflow in this repository,
pinned by tag:

```yaml
determine-runner:
  uses: peasant-labs/infra/.github/workflows/runner-router.yml@runner-router-v1
  with:
    fallback: '["blacksmith-4vcpu-ubuntu-2404"]'
  secrets:
    RUNNER_STATUS_TOKEN: ${{ secrets.RUNNER_STATUS_TOKEN }}
```

The router probes the organization runner list for an online runner carrying the
requested labels and returns `runner` (the label array for `runs-on`), `pool`
(`true` when the pool was chosen), and `reason` (`pool-online`,
`no-online-runners`, or `query-failed`). The fallback is written before the
probe, so an abort still yields a runnable label set.

Two consequences worth knowing:

- GitHub names a job that calls a reusable workflow by joining each level with
  ` / `, so the router's check run is `determine-runner / pick`. Anything that
  gates on check-run names (a release gate manifest, for example) must use that
  nested name.
- A caller may not elevate permissions: a workflow whose top-level
  `permissions:` is empty must grant `contents: read` on the calling job, which
  is what the router declares.

## Runner image

The image is built, published, and keylessly signed by a GitHub Actions
workflow in the maintainer's dotfiles repository (`runner-image`, manual
dispatch):

1. Build `modules/nixos/services/github-runner/container/Containerfile`.
2. Push `quay.io/peasant-labs/github-runner:<actions/runner version>`.
3. Resolve the digest the registry actually stores (see the Quay quirk below).
4. Sign that digest with cosign. The signature names
   `https://github.com/dayvidpham/dotfiles/.github/workflows/runner-image.yml@refs/heads/main`
   and is recorded in the Sigstore transparency log (Rekor).

The desktop module pulls the image by digest and runs `cosign verify` with that
identity and the GitHub Actions OIDC issuer before starting any container. A
stamp file records the verified reference, so a reboot needs neither the
registry nor Sigstore. The pin lives in the module's `imageRef`.

Verify a published image by hand:

```sh
cosign verify \
  --certificate-identity 'https://github.com/dayvidpham/dotfiles/.github/workflows/runner-image.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  quay.io/peasant-labs/github-runner@sha256:<digest>
```

## Quay namespace

`quay.io/peasant-labs` is a free-tier user namespace, so every repository is
public and pulls are anonymous. The robot account `peasant-labs+builder` holds
write per repository; it is the credential behind the `QUAY_ROBOT_TOKEN`
repository secret in the dotfiles repository.

| Repository | Purpose |
|---|---|
| `github-runner` | The pool image, one tag per `actions/runner` version, consumed by digest |
| `ubuntu` | Mirror of `library/ubuntu` for the tags CI uses (22.04, 24.04, 26.04) |
| `postgres` | Mirror of `library/postgres:16-alpine` (test services) |
| `caddy` | Mirror of `library/caddy:2-alpine` (local development) |

The mirrors exist because the pool's four runners share one host address, and
Docker Hub meters anonymous pulls per address. Pulls from this namespace are
anonymous and unmetered. Images whose publishers run their own registries are
not mirrored: Fedora and Arch publish on Quay, and openSUSE Leap on
`registry.opensuse.org`.

The mirrors are single-arch `linux/amd64` images. CI is amd64-only; compose
files that use them pin `platform: linux/amd64`.

### Quay quirk: digests depend on the Accept media type

Quay re-serializes a manifest when the request's `Accept` header asks for a
media type other than the stored one, and the converted form is not addressable
by digest. A `Docker-Content-Digest` header, or a client-side `RepoDigests`
value recorded at push time, can therefore name a digest that cannot be pulled.
To resolve a digest reliably, hash the served manifest bytes and require the
registry to resolve that digest before signing or pinning it.

## Related

- [`runner-router.yml`](../.github/workflows/runner-router.yml) — the shared
  router.
- [`runner-router-smoke.yml`](../.github/workflows/runner-router-smoke.yml) —
  production-path check of the router's output contract.
