# Per-job runner VMs: spike report and go/no-go

Date: 2026-09-30. Host: the maintainer's NixOS desktop.

The spike asked one question: is a host-side dispatcher built on
`actions/scaleset`, booting one Cloud Hypervisor VM per job, smaller and better
than running Actions Runner Controller (ARC) on k3s? Both options share the same
VM substrate, runner image, and pool-health router; they differ only in the
layer that turns GitHub job assignments into runners.

## Verdict

**Go** for the per-job VM dispatcher as the ephemeral-pool design. ARC on k3s
stops being an active candidate and stays the documented fallback, re-opened
only by the triggers under [When to revisit](#when-to-revisit).

**Not yet** for retiring the persistent container pool. Both pools run side by
side today; the migration gates below must close first.

## What was built

| Piece | Where | Size |
|---|---|---|
| Dispatcher (Go, `actions/scaleset` v0.4.0) | `runner-dispatcher/` | 1,546 lines, plus 1,089 lines of tests |
| Dispatcher NixOS module | `modules/nixos/services/runner-dispatcher/` | one module |
| Slot and guest config (microvm.nix) | maintainer dotfiles, `modules/nixos/virtualisation/runner-vm/` | 571 lines of Nix, module included |
| Pool-health router | `.github/workflows/runner-router.yml` | heartbeat read replaces the online-runner probe |
| Validation workflows | `runner-vm-smoke.yml`, `runner-vm-realtest.yml`, `runner-vm-routing-probe.yml` | dispatch-only |

Shape: scale set `desktop-microvm`, runner group `minttea--desktop`, labels
`self-hosted, linux, x64, microvm`, four slots (`runner-vm-1..4`), 6 vCPU and
4 GiB per VM. The runner image is baked into the guest as a tarball; the JIT
config reaches the guest over a per-slot read-only virtiofs share; module and
build caches are a shared virtiofs mount; the container store lives on a 6 GiB
per-slot ext4 volume. Credentials: a GitHub App (runner registration and the
health variable), held host-side with `LoadCredentialEncrypted`; no PAT and no
always-on runner.

## Exit criteria

| Criterion | Result | Evidence |
|---|---|---|
| JIT VM boots, runs exactly one job, exits | **Met** | Smoke runs 36713036862, 36714381445, 36715659710; every job on a unique `runner-vm-N-<hex>` runner; slot powers off after the job |
| Real workload in a VM | **Met** | Real-test runs execute the dispatcher's own `go test ./...`; all packages `ok` |
| Concurrency | **Met** | Three concurrent real-test jobs on `runner-vm-1/2/3` (runs 36729781026, 36770526296, 36782961208), all green |
| Overhead measured | **Met** | See [Latency](#latency) |
| Warm cache on a later job | **Met** | First real test downloaded six modules and took 25 s; later runs downloaded none and took 6–7 s with `-count=1` (compile cache, not test-result cache); the three concurrent jobs shared the cache without errors |
| Baked image, no per-job registry pull | **Met** | Guest log shows `image loadfromarchive` from the baked tarball, no registry traffic |
| Router: healthy pool routes to the VM labels | **Met** | Routing probe returned `pool-online` on a VM slot |
| Router: unhealthy pool falls back visibly | **Partly** | `query-failed` fallback observed; the `pool-stale` branch shares that path but was never triggered directly |
| Assignment survives dispatcher restarts | **Met, incidentally** | Assignments queued during the crash-loop period ran after the fix; unacknowledged messages were redelivered to the new session |
| SIGTERM drain during a running job | **Not exercised** | Drain logic is unit-tested; no live mid-job drain |
| Dispatcher restart mid-job; VM crash | **Not exercised** | No deliberate live test |
| Negative security tests | **Not exercised** | Isolation holds by construction (no host socket or App key in the guest; only the cache and JIT shares), but no synthetic-fixture probe has run from inside a VM |

## Latency

Healthy-path measurement on an idle pool (run 36788926641, 2026-09-30):

| Step | Clock (UTC) | Delta |
|---|---|---|
| Job queued | 23:02:13 | 0 s |
| Slot unit started (message received, JIT minted, virtiofsd reset) | 23:02:20 | +7 s |
| Guest userspace up | ~23:02:30 | +17 s |
| Baked image loaded, job unit started, dispatcher sees READY | 23:02:45 | +32 s |
| Runner takes the job | 23:02:48 | **+35 s** |
| Job done (warm Go test suite) | 23:02:54 | +41 s |

Where the 25 s from unit start to guest-ready goes: about 10 s guest boot to
userspace, then about 15 s of `podman load` from the baked tarball, on every
boot.

Baseline: the persistent container pool starts a job 1–2 s after it is queued
when a slot is free, and queues otherwise. So each VM job costs roughly
**+33 s**, which the owner accepted up front.

**Boots are serialized.** A planning cycle boots its VMs one after another,
each waiting for readiness (~21 s apiece), so the third of three simultaneous
jobs starts about 45 s after the first. This is a dispatcher limit, not a
Cloud Hypervisor one.

## Resources

Idle, the VM pool holds **no** memory: slots are stopped until a job arrives.
Under load it holds up to 4 × 4 GiB. The container pool keeps four persistent
runners resident at all times. That meets the "no constant memory reservation"
goal.

## Cost of getting here

The spike worked on the first design, but reaching a reliable run exposed a
chain of independent defects. Each needed a merge, a flake-lock bump, and a
host rebuild:

| Defect | Symptom | Fix |
|---|---|---|
| Reused JIT runner names | 409 on mint; dispatcher crash loop | Unique name per mint (#32) |
| 30 s notify start timeout | `systemctl start` killed mid-boot | 300 s client timeout (#33); 660 s stop timeout (#35) |
| Guest auto-start and restart | Slots booted without work; restarted after poweroff | `autostart = false`, `Restart = "no"` (dotfiles #25) |
| Container store on tmpfs | Image load filled memory | 6 GiB ext4 volume per slot (dotfiles #26) |
| Job unit readiness | Guest never reported ready | `Type = "exec"` (dotfiles #27) |
| No guest visibility | Failures invisible from the host | Journal forwarded to the console (dotfiles #28) |
| Job-container DNS | Resolver stub unreachable | Host networking inside the guest (dotfiles #29) |
| virtiofsd restart hook inside the start transaction | Start job deadlocked; control process killed every 3 s | Reset moved into the dispatcher as a separate call (#36; hook removed in dotfiles #37); `%%s` escape (#37) |
| Dispatcher exit on any step error | Every transient failure dropped the queue session and added minutes of redelivery delay | Retry inside the same session; cycle telemetry (#39) |
| Router token scope | Router took the fallback | PAT gained repository `Variables: read` |

Outside the dispatcher: 489 stale organization runner registrations were
deleted after the name-collision period.

About two thirds of these defects sit in the VM substrate (guest units, storage,
virtiofsd, networking). Those would exist under ARC on k3s too, because both
options run in the same VM substrate. The rest (names, session handling,
retries, telemetry) are controller work that ARC would have supplied.

## Why not ARC on k3s

What the dispatcher re-implements from ARC: the reconcile loop (assigned jobs
against live VMs), slot allocation, boot and reclaim, drain, retry with backoff,
and health reporting. It does this on the same protocol client ARC uses, in
about 1.5k lines.

What ARC on k3s would have added and we avoided owning:

- a cluster (API server, datastore, CNI, storage, RBAC, upgrades) for one node
  and one workload;
- the ARC controller and listener lifecycle, including its known silent
  listener wedge and the periodic-restart workaround;
- privileged Docker-in-Docker pods for container-using jobs, and PVC plumbing
  for caches;
- shared-kernel pods: VM-per-job isolation would still need Kata, rejected
  earlier (rootful, nested virtualisation, socket authority remains).

What ARC would **not** have removed: the guest image and boot path, the cache
and JIT shares, the idle-pool heartbeat (an idle scale set has zero registered
runners under either design), and the runner image.

## Migration gates

Before routing production jobs to the VM pool and retiring the container pool:

1. Deploy #39 and confirm a `dispatcher started` line plus `queue message` and
   `boot_seconds` lines in the journal.
2. Live drain: SIGTERM the dispatcher during a running job; the job completes and
   no VM is orphaned.
3. Live restart mid-job and a forced VM kill; the dispatcher reconciles, and the
   slot is reusable.
4. Stale heartbeat: stop the dispatcher for longer than the 300 s window; the
   router returns the fallback with the `pool-stale` reason.
5. Negative security probe from inside a VM with a synthetic fixture: no host
   podman socket, no dispatcher, no App key, no host path besides the cache and
   JIT shares, and no host service reachable over the bridge beyond NAT egress.
6. Run a representative caller workload (for example peasant `make check`)
   through the router on the VM labels, and compare its duration with the
   container pool.

## Follow-ups

- **Parallel boots** within a planning cycle, removing the ~21 s per-extra-job
  serialization.
- **Skip the per-boot image load** (~15 s): keep a pre-loaded, read-only image
  store on the slot volume and reload only when the pinned digest changes. This
  trades a fresh store per job for speed; the container's writable layer stays
  per job.
- Clear the cosmetic `failed` unit state after a guest poweroff (wrapper exit
  code) so `systemctl --failed` stays meaningful.

## When to revisit

Re-open ARC on k3s (or another scheduler) if the pool grows beyond one host,
needs several scale sets or VM classes with quotas or fairness, or if the
dispatcher's scheduling logic starts to grow faster than its VM driver. At that
point the dispatcher is turning into a general scheduler, and owning a platform
pays for itself.
