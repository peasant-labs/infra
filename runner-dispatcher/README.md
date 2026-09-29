# runner-dispatcher

A host-side dispatcher for one GitHub Actions runner scale set. It replaces
the "is a runner online?" model with GitHub's scale-set queue: the dispatcher
long-polls the queue, and for every assigned job it mints one just-in-time
(JIT) registration and boots exactly one ephemeral runner VM. Caches are
shared from the host; nothing else survives a job.

This package is being built as a time-boxed spike (see the plan record in
Beads) to compare against running Actions Runner Controller on k3s. The
hypervisor driver is not wired yet; the command currently runs against the
in-memory driver so queue handling can be exercised without booting VMs.

## Shape

```
scale-set queue ── long poll ──► dispatcher ── JIT mint ──► driver.Boot(spec)
                                     │                          │
                                     │ planner.Plan             ▼
                                     └───────────────► one-job runner VM (exits)
```

- `internal/planner` is pure: statistics plus tracked instances in, boot and
  reclaim actions out. Fixture cases live in `testdata/planner.yaml`.
- `internal/dispatcher` owns the message loop, drain, and the JIT-per-boot
  handoff. It depends only on small interfaces (`Session`, `JITSource`,
  `vm.Driver`).
- `internal/scalesetadapter` binds `github.com/actions/scaleset` to those
  interfaces and is the only package that talks to GitHub.
- `internal/heartbeat` defines the pool-health record the router reads; the
  publisher implementation lands with the router slice.
- `internal/vm/dryrun` is the in-memory driver used until the Cloud
  Hypervisor driver lands.

## Scaling rule

Demand is `statistics.TotalAssignedJobs` (waiting plus running jobs) capped by
`-max-capacity`. Busy and booting VMs always count toward demand; exited VMs
are reclaimed; surplus idle VMs are reclaimed down to demand. Messages are
acknowledged after their statistics are applied; long-poll expiries reconcile
against the last seen statistics instead of assuming zero demand.

## Running (dry run)

```sh
go run ./cmd/runner-dispatcher \
  -github-config-url https://github.com/<org> \
  -scale-set-name desktop-microvm \
  -app-client-id <client-id> \
  -app-installation-id <installation-id> \
  -app-private-key-file /path/to/app.pem \
  -max-capacity 4
```

The GitHub App needs Actions administration on the organization (runner
registration and scale-set management). On shutdown the dispatcher stops
polling, reclaims VMs without a job, waits up to `-drain-timeout` for
in-flight jobs, then exits.

## Status

- [x] Planner with fixture-driven tests
- [x] Message loop, JIT mint handoff, drain
- [x] Scale-set client adapter (create/ensure, session, JIT)
- [x] VM driver: host boot command per VM, process-group lifecycle, JIT file handling
- [ ] Cloud Hypervisor guest image and boot wrapper
- [ ] Heartbeat publisher and router integration
- [ ] End-to-end spike run and measurements
