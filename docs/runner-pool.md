# Self-hosted runner pool

Organization CI runs on a self-hosted pool with hosted runners as a fallback.
This document covers the pool, the shared router in this repository, the pool
image, and the Quay namespace that serves the images CI pulls.

Operational detail for the host itself (the NixOS module, state layout, and
resource slices) lives in the maintainer's dotfiles repository,
`docs/github-runner.md`.

## Shape

- **Pool**: the per-job MicroVM scale set on the maintainer's NixOS desktop,
  labels `self-hosted, linux, x64, microvm`. The dispatcher starts a slot for
  each job; job and service containers use the guest's own podman socket. The
  shared router targets those MicroVM labels. A caller still on an older router
  revision emits its requested label set (default `["self-hosted","container"]`)
  whenever GitHub lists an online runner carrying every requested label. That
  runner can be a persistent runner or a scale-set runner that still carries
  `container`. Once the persistent units are stopped, that caller falls back to
  hosted only while no such runner is online; while a scale-set runner is online
  it may run on a MicroVM.
- **Persistent pool**: the desktop's rootless-podman runners (`container`
  label) are a separate pool that the host retires by draining, then stopping
  the units, then switching to `enable = false`. The switch would also stop
  them, but only after the drain window. That host flip is not made by this
  repository; the module stays published for other hosts, and a host that
  retires it keeps the module importable for rollback. After the pool is
  disabled, `container` remains a transitional residue while a caller still
  routes to it; see "Retiring a host's persistent container pool".
- **Fallback**: every caller passes its own fallback label set (Blacksmith for
  the release jobs, hosted Ubuntu elsewhere). An unavailable or full pool
  returns the caller's fallback.
- **Deployment**: caller adoption, the host's own pool cutover, and the
  retirement of that host's persistent pool are separate operations, each
  confirmed by its own evidence.

## Routing

Every routed repository calls the shared reusable workflow in this repository,
pinned by a reviewed commit SHA (infra itself uses a local workflow path):

```yaml
determine-runner:
  uses: peasant-labs/infra/.github/workflows/runner-router.yml@<reviewed-40-character-commit-sha>
  with:
    fallback: '["blacksmith-4vcpu-ubuntu-2404"]'
    # Only when this run places several pool jobs at once, say how many:
    # required_slots: 4
  secrets:
    RUNNER_STATUS_TOKEN: ${{ secrets.RUNNER_STATUS_TOKEN }}
```

The router reads the dispatcher's pool-health record — the repository
variable `RUNNER_POOL_HEALTH` in this repository — and returns `runner` (the
label array for `runs-on`), `pool` (`true` when the pool was chosen), and
`reason` (`pool-online`, `pool-busy`, `pool-stale`, or `query-failed`). The
record counts as online when it is younger than five minutes, reports a healthy
listener, and has room for the whole calling run: `max_capacity -
assigned_jobs` (booting and running) must leave at least `required_slots`
free slots, or the run's jobs would wait in the pool's queue and the router
falls back instead. `required_slots` defaults to `1`, so a run that places
one job routes to the pool on any free slot. A request larger than the
pool's `max_capacity` can never fit, so it always falls back with reason
`pool-busy` and a notice saying the request exceeds the pool's capacity. A
record without `max_capacity` skips the capacity gate, so an older dispatcher
keeps routing by health alone. `pool-busy` therefore means "healthy and
fresh, but fewer free slots than `required_slots`", and the router's notice
line names the `assigned`, `max`, and `required` counts so the cause is
visible. The runner list is deliberately not consulted, because a scale set
has no registered runners while it is idle. The fallback is written before
the check, so an abort still yields a runnable label set.

The contract test is the Go test in `runner-dispatcher`
(`TestRouterReadsPublishedRecord` and `TestRouterMutationsAreKilled`), which
run this workflow's own script against records written by the dispatcher's
publisher.

Known limit: two runs that start at the same moment can both see enough free
slots and both route to the pool, so one of them still queues. The check
narrows that overflow; it cannot remove it without a reservation, which the
router does not have.

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
workflow in this repository (`runner-image`, manual
dispatch):

1. Build `runner-image/Containerfile`.
2. Push `quay.io/peasant-labs/github-runner:<actions/runner version>`.
3. Resolve the digest the registry actually stores (see the Quay quirk below).
4. Sign that digest with cosign. The signature names
   `https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main`
   and is recorded in the Sigstore transparency log (Rekor).

The desktop module pulls the image by digest and runs `cosign verify` with that
identity and the GitHub Actions OIDC issuer before starting any container. A
stamp file records the verified reference, so a reboot needs neither the
registry nor Sigstore. The pin lives in the module's `imageRef`.

Verify a published image by hand:

```sh
cosign verify \
  --certificate-identity 'https://github.com/peasant-labs/infra/.github/workflows/runner-image.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  quay.io/peasant-labs/github-runner@sha256:<digest>
```

## Quay namespace

`quay.io/peasant-labs` is a free-tier user namespace, so every repository is
public and pulls are anonymous. The robot account `peasant-labs+builder` holds
write per repository; it is the credential behind the `QUAY_ROBOT_TOKEN`
repository secret in the infra repository.

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

The `ubuntu` mirror is a byte-identical multi-arch copy (the index and its
children are preserved), so the release matrix resolves native amd64 and arm64
images from it. The `postgres` and `caddy` mirrors are single-arch
`linux/amd64` images; compose files that use them pin `platform: linux/amd64`.

### Quay quirk: digests depend on the Accept media type

Quay re-serializes a manifest when the request's `Accept` header asks for a
media type other than the stored one, and the converted form is not addressable
by digest. A `Docker-Content-Digest` header, or a client-side `RepoDigests`
value recorded at push time, can therefore name a digest that cannot be pulled.
To resolve a digest reliably, hash the served manifest bytes and require the
registry to resolve that digest before signing or pinning it.

### Draining before a rebuild

The following describes the persistent-container pool, not MicroVM slots. It
applies only while that pool's module is enabled: a host that has retired the
pool no longer has `github-runner-drain` on the PATH, so the deploy script logs
its no-drain notice instead of draining, and its pre-switch drain is step 1
of "Retiring a host's persistent container pool"; the stop and the switch are
the following steps. The MicroVM dispatcher has its own bounded drain; see the
host's operational documentation before stopping it.

Stopping a runner unit while GitHub considers it busy cancels the in-flight
job: the listener exits on SIGTERM rather than finishing, and `podman stop`
delivers that SIGTERM at the end of the module's `ExecStop`. Whether a runner
is busy is visible only in GitHub, so the module ships
`github-runner-drain` (on the system PATH, enabled with the pool) to wait for
idle before anything stops the units. It re-reads the organization runner list
every cycle and matches the pool's instance names, so runner-id rotation
(PAT rotation, `--replace`, ephemeral runners) cannot make it poll a stale id;
a runner that is not registered between jobs counts as drained. It exits 0
when no instance is busy, 1 when a `--timeout` (default 15 minutes) expires
with runners still busy, and 2 when the API cannot be reached — it never stops
anything itself.

A rebuild or a manual stop should drain first: the desktop's `switch.sh`
calls it before `nixos-rebuild`, aborts on exit 1 with a named error, and
forces past a timeout only via `SWITCH_NO_DRAIN=1`. Proceeding past exit 1
cancels the in-flight jobs, so that flag exists so the cancellation is
deliberate, never silent.

## Retiring a host's persistent container pool

Setting `CUSTOM.services.github-runner.enable = false` retires the persistent
pool. The module stays published: other hosts can run it, and a host that
retired it needs the same import for rollback. Retirement removes the second
pool, not the first: the dispatcher, the VM slots, the runner group, the
App credential, and the scale set are untouched.

### What retirement does, and what it does not

A switch to `enable = false` removes the runner units, their resource slices,
the image pull and verification units, and the `github-runner-drain` helper
from the generation. The enablement symlinks live in the generation, so the
pool does not come back after a reboot or a fresh user manager. The same switch
stops active container units that are still present when it reaches them:
stop-on-removal is the default, so the switch runs each container unit's
`ExecStop` (`podman stop`). That stop sends the SIGTERM that cancels a job the
listener is running. `Restart = always` does not revive an explicit stop, and
the container runs with `--rm`, so the stop also removes the container
instance. That switch stop is later than the drain. A `Restart = always` runner
can accept a job in the gap, and the switch would then cancel it. The sequence
is drain, and only on exit 0 stop the units, then switch.

The podman options this module sets leave with it. A host that needs podman
afterwards configures it itself, which is what the module's isolation invariant
expects of every consumer.

Retirement keeps the state directory, the pinned image, the registration token,
the runner group, and the previous system generation. Nothing in the retirement
path deletes runner state, images, volumes, credentials, or registrations.

### The label stays until the last caller migrates

Retirement does not remove the label that named the pool. It stays in the host
configuration and on the live scale set while a caller still routes to it, and
the configuration warns on every build that says so:

```
evaluation warning: CUSTOM.services.runner-dispatcher.labels still carries
"container" while CUSTOM.services.github-runner.enable is false. …
```

That warning is the reminder, not an error: the state is the prescribed
intermediate one, and it clears the moment the label is removed from the
configuration.

A caller that still selects `container` is served by the scale set, on a MicroVM
slot. A caller on the older router revision probes online runners, sees none
while the scale set is idle, and takes its own hosted fallback. Both outcomes are
reported by the caller's router step, so neither is silent.

### Labels are create-time state

Labels are applied when a scale set is created; a redeploy reuses the existing
scale set and never rewrites them. The pool-health record carries no labels
either. So a redeploy cannot fix the live label set and the router cannot see
it. Read the scale-set object named by
`CUSTOM.services.runner-dispatcher.scaleSetName` (`desktop-microvm` on the
desktop) through the actions scale-set API the dispatcher uses. The runner-group
id comes from the same lookup the dispatcher already performs,
`GetRunnerGroupByName`, against
`_apis/runtime/runnergroups/?groupName=<runnerGroup>`. The scale-set GET is
`_apis/runtime/runnerscalesets?runnerGroupId=<group id>&name=<scaleSetName>`
on the Actions service, with `api-version=6.0-preview`. That GET returns
`{count, value}`. The pass is `count == 1` and set equality of
`value[0].labels[].name` with the configured label names. Order and each
label's `type` are ignored. The raw JSON is not equal to the Nix string list. This is not `gh api` on `api.github.com`,
and it is not the organization runner list. An idle scale set has no online
runners, so those rows are not this check.

### Before retiring

1. Every routed caller pins a reviewed router revision; callers whose migration
   is deferred keep a working hosted fallback.
2. The replacement pool is proven on the host: the health read in Verify
   passes, and a real job was observed the way Verify describes.
3. No run is queued, waiting, or in progress against the labels being retired.

### Sequence

1. Drain with the installed helper, `github-runner-drain --timeout SECONDS
   --interval SECONDS`, and continue only on exit 0. The exit codes are those in
   "Draining before a rebuild"; a timeout (exit 1) and an unreadable API (exit
   2) both mean do not stop anything and do not switch. Do not use `--no-drain`
   or `SWITCH_NO_DRAIN=1` for this cutover.
2. Immediately after exit 0, stop the `github-runner-container@` units this
   host installed:
   `systemctl --user stop 'github-runner-container@*.service'`.
   The pattern is `github-runner-container@<name>-<n>` for `n` in `1..count`.
   On the desktop, `name` is `desktop-container` and `count` is 4, so the units
   are `github-runner-container@desktop-container-1.service` through
   `github-runner-container@desktop-container-4.service`. The switch also stops
   removed units, but that happens later, so a `Restart = always` runner can
   accept a job in the gap and the switch would cancel it. Leave the podman
   socket, the container-store GC timer, the intermediate image, the state tree,
   and every unrelated container alone.
3. Switch to the configuration that disables the pool. The switch removes the
   unit files and stops any container units still active; that stop is the
   `ExecStop` and `--rm` behaviour in "What retirement does, and what it does
   not". The switch also removes the drain helper, so a routine rebuild
   afterwards reports that it did not drain; the persistent pool no longer
   needs draining, and the MicroVM dispatcher keeps its own bounded drain.
4. Expect and record the evaluation warning naming the transitional label. It is
   the intended state at this point, not a failure of the retirement.

### Verify

- Immediately after a completed switch, the new generation's
  `/etc/systemd/user` has no `github-runner-container@*` files, and
  `systemctl --user list-unit-files 'github-runner-container@*.service'`
  reports `0 unit files listed.` (it exits 1). A completed switch stops the
  removed units and reloads the user manager. The loaded-unit view is not the
  verdict: an unmatched `show` glob prints nothing, and `not-found` is a
  `LoadState`, not an `ActiveState`.
- If the switch exited before its user phase, or the user manager still lists a
  unit, run `systemctl --user daemon-reload` once as the pool user and re-check.
  A specific unit's absence can then be read as
  `systemctl --user show -p LoadState --value github-runner-container@<name>-1.service`
  printing `not-found` (its `ActiveState` prints `inactive`, not `not-found`).
- As the pool user, `podman ps -a` exits 0 whether or not a container remains,
  so the pass is reading no `github-runner-<name>-*` row. `<name>` is the same
  base the sequence uses for `<name>-<n>`, default `${hostName}-container`.
  The pinned image in `podman images` and the state directory
  (`/home/<user>/.local/share/github-runner-containers` by default) are still
  present: retention is intentional.
- Read the pool-health record and require the router's freshness condition plus
  a present `max_capacity` key:
  `gh api repos/peasant-labs/infra/actions/variables/RUNNER_POOL_HEALTH --jq .value`
  piped into `jq -e --argjson now "$(date -u +%s)" '
    .listener_healthy == true
    and ((($now - (.timestamp | fromdateiso8601)) | fabs) < 300)
    and has("max_capacity")'`.
  Exit 0 is the pass: the record is younger than the router's five-minute
  window, the listener is healthy, and the record reports capacity. This check
  does not require a free slot; capacity present is what retirement needs.
- Read the scale-set object named by
  `CUSTOM.services.runner-dispatcher.scaleSetName` (`desktop-microvm` on the
  desktop) through the actions scale-set API, as in "Labels are create-time
  state". The GET returns `{count, value}`. The pass is `count == 1` and set
  equality of `value[0].labels[].name` with the configured label names — the
  MicroVM labels plus `container` while the residue is expected, and nothing
  else. Order and each label's `type` are ignored. The raw JSON is not equal
  to the Nix string list. This is not `gh api` on `api.github.com`, and it is
  not the organization runner list. An idle scale set has no online runners, so
  those rows are not this check. The heartbeat cannot show the labels and a
  redeploy cannot change them.
- In
  [`runner-vm-routing-probe.yml`](../.github/workflows/runner-vm-routing-probe.yml),
  the step "Assert the machine matches the router's decision" prints
  `reason: pool-online` and `runner: runner-vm-…`. Those two lines are the pass.
  The reusable router's `pick` job prints `reason=pool-online` and does not
  report the machine; the `=` form is not this check.
- Guest poweroff: use the slot index from that probe run's `runner:` line
  (`runner-vm-<n>-<hex>`), and the system manager, not `systemctl --user`.
  `systemctl show -p ActiveState --value microvm@runner-vm-<n>.service` exits 0
  and prints `active`, `activating` or `reloading` while the guest is still up,
  and `inactive` for a slot that never started or was stopped. After a completed
  job on this host it prints `failed`, because the guest powers itself off and
  the shutdown helper then races the already-exited process. `failed` alone is
  not the pass: a crash or OOM also fails the unit. The pass is both:
  - the unit is `failed`, and
  - this invocation's journal contains the guest's `System Power Off` line:
    `journalctl -u microvm@runner-vm-<n>.service _SYSTEMD_INVOCATION_ID="$(systemctl show -p InvocationID --value microvm@runner-vm-<n>.service)"`
  A `System Power Off` line from an older invocation is not this check.
  `systemctl is-active` prints the same state word and exits non-zero for
  `failed`; that exit is not the failure signal.
- Slot reuse is the positive reuse evidence: two probe runs' `runner:` lines
  share `runner-vm-<n>-` and differ in the hex suffix. The suffix is on the
  runner name, not the slot unit.
- Before triggering a caller still on the older router revision, read
  `gh api /orgs/<org>/actions/runners --paginate`. The pass is that no
  `.runners[]` row has a `name` matching `<name>-*` and `status == "online"`.
  `<name>` is the same base the sequence uses for `<name>-<n>`; on the desktop
  that base is `desktop-container`. The older router's idle-fallback line is
  `chosen: <fallback> (pool=false, reason=no-online-runners)`. The job's machine
  name must be a hosted name or `runner-vm-*`. An assignment to `<name>-*`, or
  a cancelled job, is a retirement failure, not the hosted fallback. Once the
  persistent units are stopped, that caller falls back to hosted only while no
  runner carrying every requested label is online; while a scale-set runner is
  online it may run on a MicroVM. Those two outcomes are not a retirement
  failure.

### Removing the transitional label (only after the last caller migrates)

1. Confirm that no caller still routes to `container`, and that no run could
   still select it.
2. Remove `container` from `CUSTOM.services.runner-dispatcher.labels` and
   switch. The evaluation warning disappears at this step.
3. The live scale set still carries the label, because labels are create-time.
   Update the scale set deliberately, and only while it is idle: recreating or
   deleting a busy scale set to change labels is forbidden. Confirm the label
   set afterwards.
4. Re-check that the callers that previously selected the label route to the
   MicroVM set or their fallback, and that the deferred caller's hosted fallback
   is still the intended path.

### Roll back

Set `CUSTOM.services.github-runner.enable = true` and switch. The state tree,
token, and runner group are intact, so the pool starts from the existing
registration and re-runs `config.sh` only when the runner state is missing or
the PAT stamp is missing or does not match. The restore does not need a new
image publish. If the switch itself is suspect, boot the previous generation,
where the pool is still enabled. Nothing needs restoring on the scale set:
retirement never removed the label.

## Related

- [`runner-router.yml`](../.github/workflows/runner-router.yml) — the shared
  router.
- [`runner-router-smoke.yml`](../.github/workflows/runner-router-smoke.yml) —
  production-path check of the router's output contract.
