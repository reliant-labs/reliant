# Machine pools

Status: Stage 1 (placement, pool-scaled concurrency) merged in reliant (#1915); Stage 7 control-plane half in review (control-plane#1689).
Stages 2–4 are designed here and not built. Owner requests 2026-10-30 16:22Z,
17:26Z (no dedicated nodes) and 17:27Z (concurrency follows the pool).

## Why

Every PR-pipeline trigger (pr-review, pr-disarm, cp-merge, sync-checkouts,
release-forge/-reliant/-cp, prod-fix-forward) names ONE machine, `cicd`
(e164d47d, 2xl). When its kata VM dies (5+ times on 2026-10-29/30, last exit
255 at 16:11Z) every in-flight review, gate and release dies with it, and every
later event queues behind a machine that is not there. A trigger can name only
one daemon (`triggers.daemon_id`), and `launch` pins the run to it.

A **machine pool** is a named set of the owner's machines. A trigger targets
the pool instead of one machine. Each launch is placed on one member and stays
there. A dead member takes only its own runs with it, and the next event lands
on a live member.

## Scope: placement only, no infrastructure dedication

Pool members are **ordinary machines**. Kubernetes schedules them on their
pinned requests = limits (cp #1591/#1603), and the cluster autoscaler adds
nodes. A pool adds no node affinity, taints, anti-affinity or node pools, and
control-plane needs no change for it (owner, 17:26Z). Everything in this design
lives in reliant's launch layer: choosing a member, its load and concurrency,
and failover.

## Non-goals

- Moving a running step between machines. A step that may have run is never
  re-run anywhere (Stage 2 re-places only steps that provably never started).
- Org-owned pools. Machines (`daemons.user_id`) are user-owned, so a pool is
  owned by the user who owns its members.
- Replacing forge heavy slots. Heavy slots throttle *commands* on one machine,
  and a pool's per-member limit throttles *runs* placed on it. They compose.

## Concepts

| Term | Meaning |
|---|---|
| Pool | `machine_pools` row: owner, `name` (unique per owner), label `selector`, `min_members`/`max_members`, `member_size`, `max_runs_per_member`, `member_idle_timeout` (default 10m) |
| Member | A machine of the pool's owner whose **durable labels** contain the selector (default `pool=<name>`) |
| Machine label | `daemons.labels` (jsonb), set by the owner (`SetMachineLabels`). It persists across sleep, reconnects and re-registration. Registration and identity upserts name their columns, so they never touch it |
| Placement | The launch-time choice of one member for one run |
| Pinned run | The chat records `machine_pool_id` + `active_daemon_id` (the member). The run's catalog pin, checkouts, skills and tool calls follow `active_daemon_id`, exactly as for a single-machine trigger |

The labels a daemon reports when it connects (`toolexec.DaemonInfo.Labels`)
vanish while it sleeps. A pool must count and wake suspended members, so
membership comes from the durable column.

## Data model

Migration `20261030222234_machine_pools`:

- `daemons.labels jsonb NOT NULL DEFAULT '{}'`
- `machine_pools (id, user_id, name, selector, min_members, max_members,
  member_size, max_runs_per_member, scale_up CHECK IN ('never'), …)`,
  `UNIQUE (user_id, name)`
- `chats.machine_pool_id` → pool (`ON DELETE SET NULL`), indexed with
  `active_daemon_id`. Together they record which member ran what, and they are
  how a member's live runs are counted.
- `triggers.machine_pool_id` → pool (`ON DELETE RESTRICT`) plus
  `CHECK (machine_pool_id IS NULL OR (daemon_id IS NULL AND NOT no_machine))`.
  A trigger runs on one machine, on none, or on a pool.

The migration is backward-compatible. A release that does not know the column
sees a pool-bound trigger with no daemon, and fails it loudly ("trigger's
daemon no longer exists") instead of running it on some other machine.

## Placement

`internal/machinepool`: `Decide(pool, members)` is the whole policy, as a pure
function. `Placer` reads its inputs (`store.ListMachinePoolMembers`: phase,
install, synced catalog, live runs, all in one query) and owns the
in-transaction slot guard.

Rules, in order:

1. **Eligible**: the project is installed on the member, it has not failed or
   been removed, and its catalog digest equals the pool's **expected catalog**.
   Each member passed over is reported with its reason.
2. Among eligible **connected** members with a free slot
   (`live_runs < max_runs_per_member`, or no limit), pick the one with the
   fewest live runs. Ties break on name, then id, so placement is deterministic.
3. Otherwise a **starting** member, in the same order. The run's preflight
   waits for it.
4. Otherwise a **suspended** member: the run is pinned to it, and the run's
   preflight wakes it (the trigger's `daemon:resume` grant is minted for every
   member).
5. Otherwise **queue**. The fire's event stays pending, with a reason such as
   `waiting for a machine in pool cicd (a: full (3/3 runs); b: disconnected)`.
   The redriver re-fires it every ~2 minutes and expires it after 24 hours.

A machine's phase is computed exactly as a trigger's `machine_state`
(attachment lease, then lifecycle), so a pool and a trigger never disagree
about a machine.

### Catalog consistency: fail closed

Each member keeps its own synced catalog (`project_machine_catalogs`, #1841),
and a run reads only its own machine's catalog (#1859). `CatalogDigest` hashes
each synced workflow's and preset's identity and content hash, sorted, so file
order and push time don't affect it.

- The **expected catalog** is the digest held by the most members that could
  run work. A tie goes to the most recent push. It is decided by majority, not
  by newest: a member that slept through merges pushes its stale catalog on
  reconnect and becomes the *newest*, so newest-wins would let one stale
  machine evict every current one.
- A member with a different digest, or one that has never synced the project,
  is **ineligible**. If no eligible member is free, the run waits. It never
  runs on a member with a different catalog.

**Every read about a pool trigger's run uses the agreed catalog.** A
pool-bound trigger has no machine of its own, so before placement the fire
binds it to a member holding the agreed catalog (`bindPoolCatalog`,
`machinepool.AgreedCatalog`). The declaration, filter, dedupe key, inputs,
prompt and concurrency policy are all read from that machine's catalog, never
from the project's union (which whichever machine pushed last decides). The
intake, both fire paths, the replay preview and activation validation do this.
If a push moves the agreed catalog between that read and placement, the fire
waits and is redriven rather than launching on decisions read from a catalog
the run would not execute. A pool none of whose members has synced the project
waits too.

`sync-checkouts` has to cover every member (Stage 4). Until then a member that
missed a merge is ineligible until it catches up. That is safe, but it reduces
capacity.

### Slots and races

`live_runs` counts the chats with `machine_pool_id = pool` and
`active_daemon_id = member` whose newest root run is live (pending, active,
paused). It is read twice: once to choose, and once inside the launch
transaction (`Spec.Guard`) after `SELECT … FOR UPDATE` on the pool row. If a
concurrent launch took the last slot, the guard declines `pool member busy`,
and the fire re-places, up to 3 times.

A resumed fire (its event already launched) is **never re-placed**. The chat
exists and is pinned to its member, and moving it would split the run from its
checkout.

## Concurrency follows the pool

`concurrency:` on a workflow (trigger admission, execution-bound,
`internal/triggers/capacity.go`) now takes either field, or both:

```yaml
concurrency:
  per_member: 3      # 3 runs for each member of the trigger's pool that can take work
  max_running: 12    # optional ceiling over the scaled cap
```

`capacityLimit` resolves it per fire:

- `max_running` alone: a fixed cap, as before.
- `per_member: K` on a pool-bound trigger: **K × eligible members** (installed,
  on the pool's catalog, not failed or removed, any of connected, starting or
  suspended). Adding a member raises throughput with no workflow edit.
- A member that drops out (dies, drifts) still counts for the slots **this
  workflow's** runs hold there (at most K): admitted, launched, unreleased
  capacity records in this trigger's admission scope whose chat the pool
  placed on that member (`CountTriggerCapacityHeldByMember`). Those runs keep
  their slot until they end, and charging them to the healthy members would
  starve the pool exactly when it has lost a machine. Other workflows' runs on
  the dead member hold none of this workflow's slots and add nothing.
- `per_member` on a single-machine or no-machine trigger is a pool of one: K.
- With both fields set, `max_running` caps the scaled value.
- A pool with no eligible member and nothing running allows 0, so fires wait
  instead of launching onto nothing.

Two limits apply, and they do different jobs:

| Limit | Scope | Role |
|---|---|---|
| `concurrency.per_member` (workflow) | one workflow, across the pool | how many reviews the pipeline runs at once |
| `max_runs_per_member` (pool) | every pool-placed run on one member | protects one machine from all workflows together |

### Recommended values for a 2xl member (30 vCPU / 64 GiB)

- **pr-review / pr-fix: `per_member: 4`.** A review is mostly LLM wall time. It
  reads the diff and runs `git`/`gh`/`rg`, never builds, and averages well
  under 1 core and about 1–2 GiB. Today 6 reviews share `cicd` with gates, so 4
  per member keeps the current total at 2 members (8 ≥ 6) and leaves room for
  gates. Raise it to 6 once one pooled week shows member CPU under 50%.
- **Gates (cp-merge, release-*): `max_running: 1` per repository, unchanged.**
  These are serialized by design (one merge batch, one release cut at a time),
  not by CPU, so they stay fixed and do not use `per_member`. On the machine,
  one `task ci` saturates a 2xl's heavy slots (min(30/2, 64/8) = 8, and a gate
  holds most of them). Plan for **1 gate per member** and let heavy slots queue
  the rest.
- **Pool `max_runs_per_member: 6`**: 4 reviews + 1 gate + 1 release/sync. That
  makes it the backstop for the case where several workflows land on one member
  at once.

## Targeting a pool

- **Trigger**: `TriggerDefinition.machine_pool_id`, exclusive with `daemon_id`
  and `no_machine` (handler validation + DB CHECK). `EnableWorkflowRequest`
  and `WorkflowTriggerOverride` carry it too. The firer places the run, and
  `launch.Spec.MachinePool` records the pool on the chat.
- **Run input**: `machine_pool = {id, name, member, members: [{id, name,
  state}]}`, runtime-injected like `session_daemon_id`. A workflow loops over
  members with a node-level `daemon: {id: …}`, which is how sync-checkouts will
  reach every member (Stage 4).

## API, agents and UI

- `DaemonRegistryService.SetMachineLabels`, `ListMachinePools` (members with
  state and live runs, plus warnings for a pool with no members or fewer than
  `min_members`), `PutMachinePool`, `DeleteMachinePool` (refused while a
  trigger uses the pool).
- `DaemonInfo.labels`, `Trigger.machine_pool_id`/`machine_pool_name`
  (`machine_state` is `pool`). A pooled run's member is its chat's
  `active_daemon_id`; exposing `chats.machine_pool_id` on `Chat` needs the
  `chats_with_activity` view to carry it (Stage 3).
- `list_machines` shows each machine's labels, its pools and the live runs each
  pool placed on it. `manage_machine` on an unattended pool run acts on the
  member its chat is pinned to.
- UI rendering of the pool and member in the Runs list, the chat header and the
  trigger view is open (Stage 3): the fields exist, the components don't use
  them yet.

## Failure handling (Stage 2, after #1889)

#1889 makes a step whose command *provably never reached* its machine
(`NotDispatched`) wait up to 30 minutes for that machine, then end the run as
`RunStepMachineUnavailable`. For a pooled run, waiting is the wrong answer when
another member is idle:

1. On `NotDispatched`, if the member is `failed`/`removed` or has not
   reconnected after a 2-minute grace, the step asks the placer for another
   member. This runs as an activity, so the choice is recorded in history.
2. If one is found, the workflow (behind `workflow.GetVersion("pool-replace")`)
   swaps its run selector, re-pins the chat, records a `machine_replaced` run
   event (old, new, step), and re-dispatches **only that step**.
3. A step whose outcome is unknown (`RunStepOutcomeUnknown`) is **never**
   re-placed. It follows #1889's path, which ends the run and frees its slot.
4. A run in a branch worktree owned by the dead member cannot move, because its
   files exist nowhere else. Pool workflows that should survive a member's
   death run in the main checkout, or create their worktree in a re-placeable
   step.

Test: the fake three-member pool. Member B dies after step 1 of 3. Step 2 is
re-placed onto A or C, step 1 is not re-sent, and an ambiguous step is not
re-sent. Stage 1 already covers the placement half
(`TestPlacement_MemberDiesMidRun`, `TestLaunchOnPool_MemberDiesMidRun`): with B
down, every new run lands on A or C, B's run keeps its pin and its capacity
slot, and B is used again once it is back and current.

## Scale-up with owner approval (Stage 5)

Creating a managed machine bills the owner, so it never happens silently.
`scale_up` admits only `never` today. Adding `ask` means: a full pool below
`max_members` raises one Inbox approval ("Pool cicd is full: add a 2xl member
at $X/h?"), priced from the control plane's catalog. Approval creates the
machine through `CreateDaemon` with the pool label, and queued events redrive
onto it once it syncs. There is no `auto` mode without a standing, priced
approval.

## Stages

| Stage | Content | State |
|---|---|---|
| 1 | labels, pools, placement, catalog check, queue/wake, trigger + launch, pool-scaled concurrency, API, `list_machines` | merged (#1915) |
| 2 | re-place never-started steps on member death | unblocked (#1889 merged) |
| 3 | UI: pool · member in Runs, chat header, trigger view; pool settings page | next |
| 4 | sync-checkouts loops `inputs.machine_pool.members`; 10-minute schedule | with the migration |
| 5 | scale-up via Inbox approval | when wanted |
| 6 | PR pipeline onto pool `cicd` | needs owner approval for machines |
| 7 | ephemeral members (`disk: ephemeral`, generic ephemeral volume, remote caches, wake-and-wait) | merged: disk mode (control-plane#1689), idle timeout on UpdateDaemon (control-plane#1697), wake-then-place (#1959), checkout restore (#1963), member idle timeout (this PR). Not started: warm caches |

## What a member costs asleep vs running

Verified 2026-10-30 against the workspace operator and prod:

- **Suspend deletes the pod and keeps the PVC** (`reconcileSuspended`,
  control-plane `internal/operators/workspace/workspace_controller.go`).
  A 2xl requests a whole n2-standard-32 kata node, and the daemon cluster runs
  the `OPTIMIZE_UTILIZATION` autoscaler profile (`deploy/kcl/lib/kata_pool.k`),
  so the emptied node and its boot disk are removed within minutes. What
  remains is the persistent disk.
- `cicd`'s disk is `pvc-17ebc5d1…`: **pd-balanced, 420 GB** (`gcloud compute
  disks describe`). pd-balanced costs $0.10/GiB-month in GCP
  (`internal/plansconfig/pricing.yaml`).
- A member auto-suspends after **30 minutes idle** (the default
  `idle_timeout`, max 4h). Every wake therefore pays for at least 30 minutes
  of compute after its last job.

GCP cost, which is what the PR pipeline costs us (list price in brackets):

| Size | Running | Asleep, persistent disk | Asleep, ephemeral disk |
|---|---|---|---|
| 2xl (30 vCPU / 64 GiB, 420 GiB disk) | $1.65/h compute + $0.06/h disk ≈ **$1.71/h** [$2.08/h] | **$42/month** ($0.06/h) [$105/month above the plan allowance] | **$0** |
| xl (15 vCPU / 32 GiB, 260 GiB disk) | $0.82/h + $0.04/h ≈ **$0.86/h** [$1.04/h] | **$26/month** | **$0** |

Sleeping is cheap either way. Disk is about 4% of a running member's cost. The
bigger idle lever is `idle_timeout`: 30 minutes of an awake, unused 2xl costs
$0.85, the same as about 14 hours of its sleeping disk. So a pool carries
`member_idle_timeout`, **10m by default**, and every member it creates or
adopts is set to it (the machine default stays 30m for interactive boxes).
Writing a pool (`PutMachinePool`) sets every managed member's idle timeout
at the control plane (`UpdateDaemon idle_timeout`, control-plane#1697, which
applies to a running machine at once), and labelling a machine into a pool
(`SetMachineLabels`) sets that machine's. A machine in several pools takes
the shortest; a machine leaving every pool keeps the timeout it has.

## Ephemeral members (Stage 7)

A CI member keeps nothing a run needs across sleeps: every run starts from
origin/main and makes its own worktree. Its disk can therefore live and die
with its pod.

**Per-pool setting `disk: persistent | ephemeral`**, defaulting to
`ephemeral` for a pool whose members are created for CI. It applies to members
the pool creates (scale-up), and the owner sets it when creating a member by
hand. `default` and other interactive dev boxes stay `persistent`: their local
worktrees and uncommitted work are the point.

### Mechanism (control-plane, workspace operator)

- `CreateDaemon` takes `disk_mode: EPHEMERAL`, which the Workspace CR carries.
- The pod mounts a **generic ephemeral volume**
  (`volumes[].ephemeral.volumeClaimTemplate`, `workspace-ssd`, the size's
  `disk_gib`, `volumeMode: Block`) instead of the named disk PVC. Kubernetes
  creates it with the pod and deletes it with the pod, so suspend leaves
  nothing to bill. Under kata it is the same virtio-blk device the docker-tier
  disk is today, formatted and mounted by the same entrypoint. This is no new
  infrastructure: same StorageClass, same nodes, same scheduling (owner,
  17:26Z).

### Why not emptyDir (owner, 20:52Z)

Measured 2026-10-30 on a kata-qemu std node in prod-daemon-v2, workspace-base
image, 4 vCPU, two rounds, then deleted (namespace `mp-disk-bench`; its PV and
GCE disk are gone). Under this cluster's kata-qemu an emptyDir reaches the
guest over **virtio-fs** (`none on /bench/emptydir type virtiofs`); the
ephemeral PVC is a **virtio-blk** device formatted ext4 in the guest.

| Workload (GOROOT tree, 15.7k files) | emptyDir (virtio-fs) | ephemeral PVC (virtio-blk) | |
|---|---|---|---|
| untar | 21–23 s | 10–11 s | 2.1× slower |
| `git add -A && commit` | 23–24 s | 4.0 s | 6× |
| `go build std`, cold cache | 51–52 s | 35 s | 1.5× |
| `go build std`, warm cache (no-op) | 2.5 s | 0.32 s | 8× |
| `git status` | 0.8 s | 0.06 s | 13× |
| `rm -rf` the tree | 5.7 s | 0.39 s | 15× |
| 1 GiB sequential write + fsync | 7–9 s | 7–9 s | same |

Git and Go builds are metadata-heavy, which is exactly where virtio-fs loses.
The workspace operator already moved the data path off virtio-fs after it
exhausted virtiofsd's file descriptors on 2026-10-09
(`internal/operators/workspace/workspace_disk.go`); an emptyDir data disk
would reintroduce both problems.

- **emptyDir on the boot disk**: virtio-fs speeds above, plus every kata
  max-tier node's boot disk (100 GB pd-balanced today) grows to ~500 GB,
  interactive machines' nodes included. Rejected.
- **emptyDir on local NVMe SSD**: $0.08/GiB-month (GCP SKU "SSD backed Local
  Storage", us-central1) ≈ $0.04/h per 375 GiB device while the node exists,
  so cheap. But node ephemeral storage still reaches the guest as an emptyDir,
  over virtio-fs; using it as a block device in the guest needs a local-PV
  provisioner and a node pool with local SSDs: new infrastructure, which the
  owner ruled out. Rejected.
- **Generic ephemeral PVC**: block-device speed (today's), $0 asleep. Its cost
  is provisioning and attaching on wake: about **15 s** measured (provisioned
  in 1 s, attached 6 s later, mapped 6 s after that), against **~3.5 min** for
  the autoscaler to add the node that every wake already waits for. **Chosen.**
- Three things that assume a persistent disk must learn the mode
  (control-plane#1689):
  1. `volume_presence` skips ephemeral workspaces, so an absent disk is
     never marked *missing → Failed*.
  2. The storage meter needs no change. The claim carries the workspace
     labels, so it bills while the pod runs; after suspend there is no Bound
     claim, so the state is StateNone.
  3. `GrowDaemonDisk` refuses an ephemeral disk (FailedPrecondition): it is
     recreated at the tier's size on every wake, and a CI member that needs
     more disk should be a larger size.
- The same PR fixes `idle_timeout` never reaching the operator (every
  machine slept after 30m whatever it asked for), so `idle_timeout: "10m"` on
  a member now takes effect.

### Warm state without a disk

| State | Source on wake | Est. time |
|---|---|---|
| Node + kata pod | autoscaler adds an n2-standard-32, image streaming | 2–4 min (same as a persistent wake today) |
| Checkouts (reliant, control-plane, forge) | blobless clone (`--filter=blob:none`) seeded from a git bundle in a GCS cache bucket, refreshed by sync-checkouts, then fetched to origin/main | 0.5–1 min (2–3 min straight from GitHub) |
| Go modules | GOMODCACHE tarball in the cache bucket, keyed by the go.sum hash (about 1.5–2 GB for the three repos) | ~0.5 min (1–2 min from proxy.golang.org) |
| Go build cache | `GOCACHEPROG` backed by the shared bucket; first-build hits stream in on demand | first gate +1–2 min vs a warm disk (10+ min fully cold) |
| forge, task, buf, linters | baked into the workspace image | 0 |

**Cold start after wake: about 5–8 minutes**, against 3–5 for a persistent
member. These are estimates; measure on the first ephemeral member before
committing pr-review latency to them. A review mostly waits on the LLM, so it
barely notices. A gate pays the first-build cost once per wake.

### Placement changes

- **Wake, then place (implemented, for every member).** A run is never
  pinned to a member that is not up. A `Wake` decision wakes the member
  (`WakeForRun`, the trigger owner's door) and leaves the event pending with
  "waking <member>"; a starting member is waited for, not pinned to, and no
  second member is woken beside it. A connected member takes runs only once
  its checkout was seen on its disk since that connection:
  `project_daemons.verified_at >= daemon_attachment.attached_at`, stamped by
  every clone and by the connect-time reconcile that finds the path. This
  needs no knowledge of the disk mode: an ephemeral member that comes up
  empty simply never verifies until its checkout is back. The redriver
  (every minute) places the event once the member is connected, verified and
  on the pool's catalog. Persistent members pay at most one redrive interval
  for it.
- **No drift.** A woken ephemeral member clones origin/main, so its catalog is
  always current. Persistent members are the ones that drift.
- **Scale to zero.** Members of a pool with `min_members: 0` all sleep when
  idle and cost $0. The first event after a quiet spell waits through one cold
  start; events in a burst land on the already-awake member. `min_members: 1`
  keeps one member always on, at $1.71/h ≈ $1,250/month for a 2xl, which
  buys away the first cold start. That isn't worth it for review latency.

### Recommendation for pool `cicd` (owner approved 2026-10-30 20:58Z)

New members are ephemeral 2xl, `idle_timeout: 10m` (the pool's
`member_idle_timeout`, default 10m), pool `min_members: 0`, `max_members: 3`. `cicd` itself stays persistent until the ephemeral path is
proven, then converts. Until Stage 7 ships, new members are persistent and
cost $42/month each while asleep.

## Migrating the PR pipeline (Stage 6)

1. The owner approves the members (size and $/member-hour). No node-pool change
   is needed: the autoscaler adds nodes for pinned requests.
2. `SetMachineLabels` `pool=cicd` on `cicd` and the new members.
   `PutMachinePool {name: cicd, max_runs_per_member: 6}`.
3. Land Stage 4 (sync-checkouts over members) and confirm that
   `ListMachinePools` shows every member on one catalog digest.
4. pr-review (and pr-fix): `concurrency: {per_member: 4}`. cp-merge and
   release-*: unchanged `max_running: 1`.
5. Rebind each activation: `EnableWorkflow … machine_pool_id: <cicd pool id>`.
6. Watch one review and one cp-merge place on a member other than `cicd`.
