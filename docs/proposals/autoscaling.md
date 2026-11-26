# Autoscaling: backend replicas on CPU, and managed Postgres storage that grows

**Status:** scope approved by the owner on 2026-11-25 (the "Settled" list
below; north star control-plane#3176, decision N7, rows HO-01 and HO-02).
Everything else here is **proposed**; the open owner decisions are collected in
[section 8](#8-open-owner-decisions) and each says what it blocks.
**Date:** 2026-11-25
**Related:** [changes.md](changes.md) (the format this follows; cohort rollouts
are Changes, not traffic splits), `internal/benefits/benefits.yaml` limit
`no-hosted-autoscaling`, `deploy/hosting` skill, control-plane
`deploy/kcl/lib/registry.k` ("No HPA … a forge gap to fix in forge").

## Summary

A workload declares a **range** instead of a count: `autoscale =
forge.Autoscale {minReplicas = 2, maxReplicas = 6, cpuTargetPercent = 70}`.
forge's one workload renderer (`pkg/deploy.RenderWorkloads`) then emits an
`autoscaling/v2` HorizontalPodAutoscaler beside the Deployment and stops stating
`spec.replicas`, so nothing forge applies fights the autoscaler. The same
declaration works on a cluster the user runs (forge applies the HPA) and on
Reliant hosting (control-plane's workload operator, which already calls the same
renderer, applies it). Admission, the namespace quota and the cost estimate
charge an autoscaled workload at its **max**; billing needs no new meter,
because hosted compute is already metered from live ready replicas.

A managed database declares how far its storage may grow:
`storageAutogrow = {maxGiB = 50}` on the `ManagedDatabase` spec. On Reliant
hosting, the managed-database operator watches how full each instance's volume
is and raises the CloudNativePG `spec.storage.size` in steps before Postgres
runs out of disk, never past `maxGiB`, never shrinking, and says so in the
activity stream and the deploy status.

## Settled (owner, 2026-11-25)

1. In: backend (service and worker) **min–max replicas on CPU** (HO-01).
2. In: managed Postgres **storage autogrow** (HO-02).
3. Out: **scale-to-zero** (no cold-start path, no request-buffering proxy).
4. Out: **database compute autoscale** (instance shape stays declared).
5. Out: **canary / traffic-split rollouts** (N26, HO-10). Changes covers cohort
   rollouts; the limit is published.
6. No auto-rollback: forge rolls forward (N1).

These are not reopened below.

## 1. Problem and experience

Today a hosted backend runs exactly the replica count its env declares
(`kcl/workload.k:705`, `replicas: int = 1`), and forge's benefits catalogue
says so: limit `no-hosted-autoscaling` (`internal/benefits/benefits.yaml:5456`),
"a managed database's storage and compute do not grow on their own". Its advice
for anyone who needs elasticity, "bind the workload to a cluster you run and
give it your own HorizontalPodAutoscaler", does not actually work today: forge
applies with `kubectl apply --server-side --force-conflicts`
(`internal/cluster/cluster.go:2417-2435`) and always states `spec.replicas`
(`pkg/deploy/workload_render.go:463`), so every deploy takes the replica count
back from the user's HPA. Our own platform hit the same wall:
`control-plane/deploy/kcl/lib/registry.k:161` records "No HPA … `replicas` is a
fixed number rather than the elasticity the ADR describes".

A managed database has a declared `storageGiB` (default 10, ceiling 500;
`pkg/deploy/v1alpha1/manageddatabase_types.go:75-81,226-227`). When it fills,
Postgres stops accepting writes, and the only fix is a person noticing and
raising `storageGiB`.

**What a person or agent writes:**

```kcl
api = forge.workloads.Service {
    name = "api"
    resources = {cpu = "500m", memory = "2Gi"}
    autoscale = forge.Autoscale {
        minReplicas = 2
        maxReplicas = 6
        cpuTargetPercent = 70   # default 70
    }
}

db = forge.ManagedDatabase {
    name = "app"
    spec = {storageGiB = 10, storageAutogrow = {maxGiB = 50}}
}
```

**What they see:**

- `forge env deploy --dry-run` and `forge env shape` print the range and its
  cost: "api: 2–6 × 500m/2Gi, $X–$Y/month at full use".
- `forge env status`, `forge obs workloads` and the console show "2–6 (3 now),
  CPU 64% of 70%", the last scaling event, and for the database "10 GiB
  declared, 13 GiB live (grew 2026-12-02, 81% full)".
- An agent handed a latency alert can read whether the workload is pinned at
  `maxReplicas` (raise it, or the shape) or nowhere near it (the cause is
  elsewhere).

**What does not change:** a workload without `autoscale` renders, applies and
bills exactly as today. `replicas` keeps its meaning.

## 2. What the code does today (verified)

| Area             | Today                                                                                                                                                                                                                                                                                                                     | Consequence for autoscaling                                                                                                                |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Declaration      | `replicas` on the author schema (`kcl/workload.k:705`, checks `:783`, `:844-845`, `:856-857`), lowered by `_spec` (`kcl/render.k:1811-1844`) into `tiers.Workload`, which is **generated** from `pkg/deploy/v1alpha1.WorkloadSpec` (`workload_types.go:140-151`; `kcl/tiers/tiers_gen.k:1-12`). The CRD is the same type. | A new field starts in Go, then the tiers KCL is regenerated; control-plane's Workload CRD gets it by pinning forge.                        |
| CPU request      | Always present after defaulting: 250m / 1 GiB, limit = request when unset (`pkg/deploy/v1alpha1/common.go:341-389`). Hosted standard plans hold requests to a shape band (`pkg/deploy/shape.go:57`).                                                                                                                      | An HPA on CPU utilization always has a denominator. With limit = request, utilization cannot pass ~100%, so a target must be below it.     |
| Runtime masks    | host refuses `replicas != 1` (`kcl/render.k:2112`); compose (`:2042-2053`) and build-only (`:2055-2060`) refuse it; hosted restricts neither `replicas` nor `resources`, and already refuses `podAnnotations` because they drive "autoscalers … the hosted platform owns" (`kcl/tiers/tiers_gen.k:814`).                  | `autoscale` takes the same masks as `replicas`.                                                                                            |
| Cluster render   | `renderLongRunning` states `spec.replicas` (`workload_render.go:463`), states 25%/25% rolling update (`:180`, `:501-505`), adds hostname spread (`:459`) and a PDB with `maxUnavailable: 1` only when replicas > 1 (`:531-542`).                                                                                          | Must omit `spec.replicas` when autoscaled; spread and PDB must key on `maxReplicas > 1`, not on a count that now moves.                    |
| HPA anywhere     | None. `autoscaling/v2` appears nowhere in forge. HPA is only in kind tables: `internal/cluster/chart_namespace.go:37`, `internal/cluster/owned_prune.go:104`.                                                                                                                                                             | Prune already handles a removed HPA on clusters.                                                                                           |
| Overrides        | A bundle override can set `{"Deployment/api" = {spec.replicas = 10}}` (`kcl/tests/positive_bundle_overrides.k:38`).                                                                                                                                                                                                       | Must be refused beside `autoscale`.                                                                                                        |
| Hosted path      | forge records `forge.dev/v1alpha1` Workload records in the env bundle; control-plane applies them (`internal/deploytarget/hosted.go:1-32`). The operator renders them with **forge's** `RenderWorkloads` under `ProfileRestricted` (`control-plane/internal/operators/workload/controller.go:167,592`).                   | One renderer change serves both bindings.                                                                                                  |
| Hosted kind set  | Closed: Deployment, Job, Service, PVC, ServiceAccount, NetworkPolicy, PodDisruptionBudget; anything else fails "the hosted platform does not apply" (`control-plane/.../workload/decorate.go:134-191`).                                                                                                                   | HPA must be added, with checks.                                                                                                            |
| Hosted apply     | Server-side apply with `ForceOwnership` under the tier field manager (`control-plane/internal/operators/shared/tier.go:45-46`).                                                                                                                                                                                           | Same fight as on clusters; same fix.                                                                                                       |
| Lapse / suspend  | `holdForLapse` merge-patches `replicas=0` under its own owner; `resumeFromLapse` relies on the normal apply to bring replicas back (`control-plane/.../workload/lapse.go:23-25,98,109-112`).                                                                                                                              | An active HPA would contest a `replicas=0` patch, and with replicas no longer applied resume must write `minReplicas` itself: section 4.2. |
| Readiness        | Reads the **live** `spec.replicas` and observed updated/ready counts (`controller.go:973-976,1314-1395`; `deployrollout/rollout.go:446-449`); `workloadhealth` is Degraded when ready < desired (`workloadhealth/health.go:14-30`).                                                                                       | Already tolerates a moving count.                                                                                                          |
| Release health   | Per-service aggregates over before/after windows gated by `min_requests`, plus `workload_restarts` (`pkg/releasehealth/releasehealth.go:19-80`). No per-replica or fixed-count assumption.                                                                                                                                | No change. A scale-in termination is not a restart.                                                                                        |
| Admission        | Demand is request × `replicas` (`control-plane/internal/deployspec/spec.go:322-341`); PAYG ceiling 2000m / 8 GiB / 50 GiB storage (`planlimits/deploy_admission.go:78-92,122-141`).                                                                                                                                       | Must count `maxReplicas`.                                                                                                                  |
| Namespace quota  | `DeriveDeployNamespaceQuota` = ceiling × rollout surge 2 for CPU/memory, ceiling + build cache for storage (`control-plane/internal/operators/shared/quota.go:436-470,158`).                                                                                                                                              | If admission counts the max, the quota already fits it.                                                                                    |
| Claimable        | 1 replica, 125m, 128Mi (`planlimits/claimable.go:45-53,109`), enforced at `computegate/claimable.go:238-240`.                                                                                                                                                                                                             | Autoscale with `maxReplicas > 1` is refused for claimable deploys.                                                                         |
| Billing          | Live meter bills each Deployment's `status.readyReplicas` × requests per sweep (`control-plane/internal/billing/inframeter/sweeper.go:127-178`). A shadow v2 meter bills each Ready pod at max(request, used CPU) via metrics-server (`inframeter/v2_compute.go:25-63`).                                                  | Replica-hours are already what we bill. No new meter.                                                                                      |
| Tenant cluster   | GKE Standard, cluster autoscaler on (`deploy/gcp/provision.sh:1363,1382`; `kata_pool.k:116,136`). Hosted workloads run under gVisor (`hosted_deploy.k:415-419`) on a pool of 0–8 nodes (`gvisor_pool.k:63-64`). Nothing disables metrics-server; k3d dev/e2e disable it (`deploy/k3d-v2.yaml:152`).                       | metrics-server for gVisor pods must be proven (slice 0). Scale-out can wait minutes on a node. e2e needs metrics-server enabled.           |
| Managed DB       | In-cluster CloudNativePG, not Cloud SQL (`control-plane/internal/operators/manageddatabase/doc.go:4-5`). Size from `storageGiB`; never shrinks (`keepExistingStorage`, `manageddatabase/claimable.go:169-240`, used at `controller.go:400`). No explicit StorageClass. Claimable fixed at 256 MiB incl. WAL.              | Autogrow raises `spec.storage.size` on the Cluster; `keepExistingStorage` already keeps a grown size.                                      |
| Disk signal      | Only `cnpg_pg_database_size_bytes` is collected (logical size, no WAL, not volume fill; `deploy/cnpg/cloudnative-pg.yaml:147`, `otel_agent.k:861`). No `kubelet_volume_stats_*`.                                                                                                                                          | Autogrow needs a volume-fill signal that does not exist yet.                                                                               |
| Resize primitive | `shared.ConvergePVCSize`: grow-only, classifies refusals as QuotaExceeded / Unsupported StorageClass / Failed (`control-plane/internal/operators/shared/pvcsize.go:16-76`), used by workspace disks.                                                                                                                      | Its refusal classification is reusable; for CNPG the size goes on the Cluster, not the PVC.                                                |
| Storage billing  | Provisioned GiB, written or not, 10¢/GiB-month (`plansconfig/pricing.yaml:53`; `hosted_usage_policy.go:51`).                                                                                                                                                                                                              | Every grow raises the bill. Whether DB volumes feed the same pool is unconfirmed (slice 0).                                                |

## 3. Architecture: forge

### 3.1 Schema

A new optional field on `pkg/deploy/v1alpha1.WorkloadSpec`:

```go
// Autoscale replaces Replicas with a range a HorizontalPodAutoscaler moves
// within on CPU utilization. Service and worker kinds only.
// +optional
Autoscale *Autoscale `json:"autoscale,omitempty"`

type Autoscale struct {
    // +kubebuilder:validation:Minimum=1
    MinReplicas int32 `json:"minReplicas"`
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=<platform cap, decision 3>
    MaxReplicas int32 `json:"maxReplicas"`
    // Average CPU use as a percent of the CPU request. Default 70.
    // +kubebuilder:default=70
    // +kubebuilder:validation:Minimum=10
    // +kubebuilder:validation:Maximum=95
    CPUTargetPercent int32 `json:"cpuTargetPercent,omitempty"`
}
```

Regenerate `kcl/tiers/tiers_gen.k` (`TestTierKCLIsCurrent`), add `autoscale?:
tiers.Autoscale` to the author `schema Workload` in `kcl/workload.k`, map it in
`_spec`, and re-export it as `forge.Autoscale`. Precedents for an optional
nested schema with its own `check:` block: `telemetry?: Telemetry`
(`kcl/schema.k:1306`) and `OpenBaoHA` (`kcl/schema.k:6074-6099`).

Validation, in `WorkloadSpec.Validate` and mirrored in the KCL `check:` blocks,
each with a `kcl/tests/negative_*.k` file and `# expect:` lines:

- `minReplicas <= maxReplicas`.
- Kind is `service` or `worker`. An operator elects one leader, so more pods
  buy nothing; batch and tool kinds have no Deployment.
- `replicas` stays at its default 1 ("autoscale owns the count; set
  `autoscale.minReplicas`").
- Not beside `storageGiB` (a ReadWriteOnce volume, the same reason `storageGiB`
  requires `replicas == 1`) or `strategy = "Recreate"` (declared as a
  correctness bound on the count).
- A CPU limit at or below the request caps utilization near 100%; a
  `cpuTargetPercent` above 95 is refused for that reason.
- No bundle override may touch `Deployment/<name>` `spec.replicas` or the
  workload's HPA.
- Runtime masks: host, compose and build-only refuse `autoscale` exactly as
  they refuse `replicas`. Cluster accepts it. Hosted accepts it when the control
  plane advertises it (3.4).

**Semver.** The field is optional and absent from every existing declaration,
so every project renders byte-identically: an additive minor (`feat(deploy):`).
The new checks fire only on the new field, so no pinned consumer's gate turns
red; KCL `check:` blocks are not lint rules and need no rollout-table row. The
field names become a permanent public surface on release (decision 1).

### 3.2 Render (`pkg/deploy/workload_render.go`)

When `Autoscale` is set, `renderLongRunning`:

- **omits `spec.replicas`** from the Deployment;
- emits `HorizontalPodAutoscaler <name>` (`autoscaling/v2`), `scaleTargetRef`
  the Deployment, `minReplicas`, `maxReplicas`, one `Resource` metric (`cpu`,
  `Utilization`, `averageUtilization = cpuTargetPercent`), and a `behavior`
  block **stated, not defaulted** (the same reasoning as the stated rolling
  update at `:494-500`): scale-down stabilization 300s, scale-up with no
  stabilization and at most doubling per 60s;
- keys the hostname spread and the PDB on `maxReplicas > 1`. A PDB with
  `maxUnavailable: 1` never blocks a drain at one pod, so rendering it from the
  max keeps the object stable while the count moves.

A `Resource` (pod-level) metric rather than `ContainerResource` keeps sidecars
in the sum, which is what the pod's request covers.

### 3.3 Apply on a cluster the user runs (`internal/cluster`)

Dropping `spec.replicas` from a server-side apply whose field manager owned it
**removes the field**, and the API server defaults it to 1: the first autoscaled
deploy of a running 4-replica service would cut it to one pod. The documented
fix is an ownership handover, which forge does before the main apply when the
live Deployment exists and forge's manager owns `spec.replicas`:

1. apply only `spec.replicas = clamp(live, min, max)` under a separate field
   manager, `forge-replicas-handover`;
2. apply the render (without `spec.replicas`) as usual; the field now belongs
   to the handover manager, so it is not removed, and the HPA writes it through
   the scale subresource from then on.

Removing `autoscale` again is the reverse and needs no step: the render states
`spec.replicas`, `--force-conflicts` takes it back, and `owned_prune`
(`owned_prune.go:104`) deletes the HPA. The repair-by-hand precedent is
`internal/cluster/strategy_recovery.go`.

The HPA needs metrics-server (the `metrics.k8s.io` API). `forge env deploy`
checks that the API is served on the target cluster before applying an
autoscaled workload and refuses with the fix when it is not (k3d ships it unless
`--disable=metrics-server`; GKE, EKS and AKS ship it). `forge env status`
reports an HPA whose metrics are unknown as degraded with that reason.

### 3.4 Hosted capability

An older control plane ignores unknown JSON (`internal/deploytarget/hosted.go:423`),
so an autoscaled workload sent to it would silently run at one replica. A new
bundle capability, `CapabilityAutoscalesWorkloads = "autoscales-workloads"`
(beside `CapabilityEnforcesPromotionChecks`, `pkg/release/bundle_features.go:264`),
is advertised by a control plane that applies HPAs; forge refuses the deploy
when it is missing, the way `limitDeclaredShape` refuses an unenforced delivery
policy (`hosted.go:580-600`). The same pattern covers `storageAutogrow` with
`CapabilityAutogrowsDatabaseStorage`.

### 3.5 Database storage autogrow field

On `ManagedDatabaseSpec`:

```go
// StorageAutogrow lets the platform raise each instance's volume, in steps,
// before it fills. Hosted only. StorageGiB stays the floor.
// +optional
StorageAutogrow *StorageAutogrow `json:"storageAutogrow,omitempty"`

type StorageAutogrow struct {
    // The most each instance's volume may grow to. At most MaxDatabaseStorageGiB.
    MaxGiB int32 `json:"maxGiB"`
}
```

`maxGiB >= storageGiB`; refused on a claimable database (fixed 256 MiB) and on
any runtime but hosted, because on a user's cluster forge runs no controller
that could act on it (decision 6). Threshold and step are platform constants in
v1, not fields: a field is forever, and we have no data yet on what to tune.

### 3.6 Docs, benefits, skills

- `benefits.yaml`: narrow `no-hosted-autoscaling` to "hosted backends do not
  scale to zero, and a managed database's compute does not grow"; add a benefit
  for `forge.Autoscale` with a render test as evidence. Add a row to the
  `compare` skill's "Choosing between them" table (scale-to-zero is what
  Vercel, Cloud Run and Fly have and we do not).
- `deploy/hosting`, `deploy/model` and `deploy/overrides` skills: the field,
  its masks, the handover, that admission charges the max.

## 4. Architecture: control plane

1. **Pin forge** with the new types; the Workload and ManagedDatabase CRDs pick
   up the fields.
2. **Kind set** (`decorate.go`): admit `HorizontalPodAutoscaler` when its
   `scaleTargetRef` is the same workload's Deployment, its only metric is CPU
   `Resource`, and `maxReplicas` is within the org's cap. Labels as for the
   other kinds. The orphan sweep already deletes HPAs (`orphansweep.go:127`).
3. **Handover** under the tier field manager, as in 3.3.
4. **Admission at the max**: `deployspec.Demand` counts `maxReplicas` ×
   request for an autoscaled workload (decision 2). The namespace quota then
   needs no change: it is derived from the same ceiling × surge 2.
5. **Claimable**: the computegate replica check reads `maxReplicas`; the
   refusal says to claim the app to autoscale.
6. **Lapse and suspend** (section 4.2): a suspended workload has no HPA.
   `suspendCompute` deletes the workload's HPA before it writes `replicas = 0`,
   and `holdForLapse` applies nothing, so nothing re-creates it during the
   lapse. `resumeFromLapse` writes `replicas = minReplicas` through the scale
   subresource before the normal apply re-creates the HPA. The protocol never
   relies on the HPA's own behaviour at zero replicas.
7. **Advertise** `autoscales-workloads` only once 2–6 ship.
8. **Status**: the deploy status gains additive proto fields for the range,
   the current and desired counts, current CPU vs target, and the HPA's
   `AbleToScale` / `ScalingActive` / `ScalingLimited` conditions;
   `workloadhealth` reports "at max" (`ScalingLimited`) as a warning, not
   Degraded, and an HPA that cannot read metrics as Degraded.
9. **Capacity**: scale-out lands on the gVisor pool (0–8 nodes,
   `gvisor_pool.k:63-64`), so a burst past current nodes waits on the cluster
   autoscaler (minutes). An alert on pending tenant pods older than 5 minutes,
   and on the pool at its max, is part of slice 2.
10. **e2e**: enable metrics-server in `deploy/k3d-v2.yaml` (or install it into
    the e2e cluster only), and add a scale test to `e2e/hosted_deploy`: load
    raises the count, it falls back after stabilization, and the bill rows
    follow `readyReplicas`.

### 4.1 Storage autogrow in the managed-database operator

- **Signal**: volume fill per instance PVC (decision 5).
- **Rule** (constants in v1): when an instance volume is ≥ 80% used, raise the
  Cluster's `spec.storage.size` by max(25%, 2 GiB), rounded up to whole GiB,
  capped at `maxGiB`; at most one grow per database per 30 minutes while a
  resize is in flight. CloudNativePG resizes the PVCs (`resizeInUseVolumes`),
  online on pd-balanced.
- **Where the grown size lives**: `status.storage.grownGiB` on the
  ManagedDatabase, and the render takes max(spec, grown, existing). The
  existing `keepExistingStorage` already never shrinks, so the declared
  `storageGiB` stays the floor and the live size is reported beside it, never
  written back to git.
- **Admission and quota**: an autogrowing database is admitted at `maxGiB` ×
  instances against the storage ceiling (50 GiB PAYG), so the namespace's
  `requests.storage` quota already has room. Otherwise a grow is refused as
  `PVCGrowQuotaExceeded`, which the code treats as a platform bug
  (`pvcsize.go:16-24`).
- **At the cap**: an activity event and an alert ("app is 92% full at its
  50 GiB maxGiB"); nothing else happens.
- **Notice**: every grow writes an activity event (old size, new size, fill)
  and appears in `forge env status` and the database panel.
- **StorageClass**: the operator sets no class today, so it is the cluster
  default; slice 0 confirms that class allows expansion on `prod-daemon-v2`, and
  the operator pins it explicitly in slice 4 rather than relying on the default.

### 4.2 Lapse and suspend with an HPA

Today `suspendCompute` merge-patches the Deployment's `spec.replicas = 0` under
`lapseFieldOwner`, and `resumeFromLapse` relies on the next apply, under the
tier field manager with `ForceOwnership`, to take `spec.replicas` back
(`control-plane/internal/operators/workload/lapse.go:23-25,98-112`). Both halves
break once the workload is autoscaled:

- The HPA is a second writer of the same field. Whether it leaves a target at
  zero alone is HPA controller behaviour (`ScalingActive=False`,
  `ScalingDisabled`), not a contract the platform controls. Entitlement loss
  must stop compute whatever the HPA controller version does.
- The render no longer states `spec.replicas`, so the apply on resume never
  writes it, and the Deployment would stay at zero.

The protocol takes the HPA out of the picture for as long as compute is
suspended, for both reasons `holdForLapse` handles: a lapsed plan and the
owner's stop.

1. **Suspend.** `suspendCompute` first deletes the HPA named for the workload,
   in the workload's namespace, if one exists (NotFound is success; any other
   error fails the reconcile, which retries, and `replicas = 0` is not written
   until the delete succeeds). It then writes `replicas = 0` through the
   Deployment's **scale subresource** under `lapseFieldOwner`. It no longer
   merge-patches the Deployment: the scale subresource is the path the HPA
   writes through, so one write path serves both cases.
2. **During the lapse.** `holdForLapse` renders and applies nothing, so
   nothing re-creates the HPA. Every resync re-asserts the suspension: it
   deletes an HPA that has appeared (one a person created by hand, say) and
   writes zero again if the count moved.
3. **Resume.** Once entitled, `resumeFromLapse`, which runs before the apply in
   the same reconcile, writes `replicas = minReplicas` through the scale
   subresource under `lapseFieldOwner` when the workload is autoscaled. The
   normal apply then re-creates the HPA, which finds a running target and
   takes over the count from there. A workload that is not autoscaled is
   unchanged: the render states `spec.replicas` and the tier manager's forced
   apply takes the field back, as today.
4. **Field ownership.** For an autoscaled workload, no applied manifest owns
   `spec.replicas`; the scale-subresource writers (the HPA, `lapseFieldOwner`,
   and forge's `forge-replicas-handover`) take turns, and none of them is
   removed by an apply that omits the field. Removing `autoscale` from a
   suspended workload needs no special case: the render states `spec.replicas`
   again, and the forced apply on resume takes it.

Tests (envtest, beside the existing `lapse_test.go` cases), each for both the
lapsed-plan and the owner-stop reason:

- suspending an autoscaled workload deletes its HPA, leaves the Deployment at
  zero, and a later resync during the lapse neither re-creates the HPA nor
  moves the count; an HPA created during the lapse is deleted again;
- a failed HPA delete fails the reconcile and does not write zero (fake client
  error injection);
- resuming sets `replicas = minReplicas` before the apply, the apply re-creates
  the HPA with the rendered spec, and `managedFields` shows `spec.replicas`
  owned by `lapseFieldOwner`, not by the tier manager;
- resuming a workload whose `autoscale` was removed during the lapse ends with
  the rendered `replicas` and no HPA;
- a workload that is not autoscaled keeps today's behaviour (the existing
  tests, unchanged).

The k3d `e2e/hosted_deploy` scale test in slice 2 adds one lapse and resume of
an autoscaled service with metrics-server running, and checks that no pod runs
during the lapse.

## 5. Billing, quotas and cost

- **Compute** is already billed from live `readyReplicas` × requests
  (`sweeper.go:127-178`), so an autoscaled workload bills its replica-hours with
  no meter change. The v2 shadow ledger is per Ready pod too. Pricing is
  unchanged (`pricing.yaml`).
- **Storage** is billed on provisioned GiB, so a grow raises the bill by
  10¢/GiB-month per instance from the moment it lands.
- **Cost visibility**: `env shape`, `env deploy --dry-run` and the console show
  the min–max monthly range at the current shape and the database's
  `storageGiB`–`maxGiB` range. `hostingbudget` has only a store and no cap
  logic; compute lapse remains the stop. No new budget mechanism in v1.
- **Abuse**: claimable deploys cannot autoscale or autogrow. A paid org's
  exposure is bounded by admission at the max, inside the PAYG ceiling (2000m /
  8 GiB / 50 GiB unless raised), and by a platform cap on `maxReplicas`
  (decision 3).

## 6. Console and agent tools

- The reliant web app already shows replicas (`services/forge/cloudEnvs.ts`,
  `RepoMetaContext.tsx`, `services/controlPlane/observability`). It shows the
  range, the current count, CPU against the target, and recent scaling events,
  from the status fields in 4.8; the database panel shows declared and live
  size, fill and grow events.
- `forge obs workloads` and `forge env status` print the same; `forge mcp`
  returns them as fields, so an agent can tell "pinned at max" from "idle".
- Changing the range is a declaration change and a deploy. No console control
  writes the range in v1, keeping git the source of truth.

## 7. Alternatives considered

| Alternative                                                                                        | Why not                                                                                                                                                                                                                                            |
| -------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Control-plane-only HPA (the operator adds one per hosted workload from a platform setting)         | Hosted-only, invisible in the declaration, and leaves the self-hosted path fighting HPAs. The operator already renders through forge's `RenderWorkloads`, so putting it in forge costs nothing extra and serves both bindings.                     |
| Bring-your-own HPA, with forge just leaving `spec.replicas` alone (a `replicas = "external"` mode) | Fixes the fight but is not a primitive: hosted users cannot apply an HPA (closed kind set), and nothing knows the max for admission, quota, cost or PDB. Could be added later beside `forge.Autoscale` if a self-hosted user needs custom metrics. |
| KEDA                                                                                               | A second operator on every cluster, CRDs we would have to vendor and admit on hosting, and its value (event sources, scale-to-zero) is the part the owner ruled out.                                                                               |
| Memory or custom metrics in v1                                                                     | Memory rarely falls when load falls (caches, Go heap), so memory HPAs flap or never scale in; custom metrics need an adapter on the tenant cluster. The `metrics` list shape leaves room for both later.                                           |
| Vertical Pod Autoscaler                                                                            | Changes the billed shape under the user and conflicts with the hosted shape band. Out of the approved scope.                                                                                                                                       |
| Admission at `minReplicas`, letting the quota cap the HPA                                          | Admits deploys whose max could never run; the HPA would hit the ResourceQuota and pods would fail to create with no clear owner of the error. Kept as an option in section 8.                                                                      |
| Keep the HPA through a lapse and rely on it ignoring a target at zero replicas                     | That is HPA controller behaviour, not a contract: entitlement loss must stop compute whatever the controller does, and resume would still need to write the count. Deleting the HPA (section 4.2) leaves one writer during a lapse.                |
| Database autogrow as an alert only                                                                 | Cheapest, and still leaves a person on the critical path at 3am. Kept as the fallback if the signal work slips.                                                                                                                                    |
| Cloud SQL for tenant databases (built-in storage increase)                                         | Managed databases are in-cluster CloudNativePG by design (backups, restore and isolation are built on it). A different engine for one feature is not proportionate.                                                                                |

## 8. Open owner decisions

None of these is decided. Per project rules, the owner decides architectural
choices; each row gives the realistic options, a recommendation, and what it
blocks.

| #   | Decision                                | Options                                                                                                                                                                                                                                                                                                                                                                                          | Recommendation                                                                                                                                                                                                                                                                             | Blocks                                          |
| --- | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------- |
| 1   | Field names (permanent public surface)  | (a) `forge.Autoscale {minReplicas, maxReplicas, cpuTargetPercent}`; (b) `{min, max, target}` as the north star sketched; (c) HPA's exact names (`targetCPUUtilizationPercentage`).                                                                                                                                                                                                               | (a): `minReplicas`/`maxReplicas` are what agents already know from HPA and match the CRD's camelCase; `cpuTargetPercent` names the metric so memory can join later without renaming.                                                                                                       | Slice 1 (the Go type and KCL schema).           |
| 2   | What admission and quota charge         | (a) `maxReplicas` × request; (b) `minReplicas` × request, the quota caps the HPA; (c) min, plus a separate per-org "burst" allowance.                                                                                                                                                                                                                                                            | (a). Every replica the HPA may ask for is guaranteed to fit, the existing quota derivation covers it unchanged, and the cost range shown is the real exposure. Cost: a smaller max fits under the PAYG ceiling.                                                                            | Slice 2 (admission, quota, cost estimate).      |
| 3   | Platform cap on `maxReplicas`           | (a) a fixed cap in forge's validation (e.g. 10, like `MaxDatabaseStorageGiB = 500`) plus the plan ceiling; (b) the plan ceiling only; (c) a per-plan cap in `planlimits`.                                                                                                                                                                                                                        | (a) at 10 for v1: forge refuses at render with a clear message, the ceiling still binds, and it bounds blast radius on a 0–8-node gVisor pool. Raise it in a minor when capacity allows.                                                                                                   | Slice 1 validation, slice 2 admission.          |
| 4   | Worker kind                             | (a) service and worker; (b) service only in v1.                                                                                                                                                                                                                                                                                                                                                  | (a). A CPU-bound queue consumer scales on CPU as well as a service does, and the render is identical. Workers with a per-replica budget (reliant's worker egress budget, `stack.k:302`) simply do not opt in.                                                                              | Slice 1 kind check.                             |
| 5   | Volume-fill signal for storage autogrow | (a) kubelet `kubelet_volume_stats_used_bytes`/`capacity_bytes` scraped by the platform's per-node otel agent, read by the operator from the metrics store; (b) the operator reads kubelet `/stats/summary` through the API server node proxy (needs `nodes/proxy`, a known escalation path); (c) a CloudNativePG exporter query (`pg_database_size` + `pg_ls_waldir()`), logical not filesystem. | (a). Fill as the filesystem sees it, no new privileged Kubernetes permissions for the operator, and the metric also feeds a "disk filling" alert for databases without autogrow. Cost: the operator depends on the metrics path, so a missing sample must mean "do nothing", never "grow". | Slice 4 (the evaluator).                        |
| 6   | Autogrow default and scope              | (a) opt-in, `storageAutogrow = {maxGiB}` declared; (b) on by default for hosted paid databases with `maxGiB = min(4 × storageGiB, 500)`, opt-out; (c) opt-in, and also a forge-run controller for self-hosted clusters.                                                                                                                                                                          | (a) for v1: every grow raises a bill, so the cap should be a number the user wrote. Pair it with the fill alert for every database, so a database without autogrow still warns before it fills. (b) is a reasonable later default once grows are proven.                                   | Slice 4 schema and defaults; the hosting skill. |
| 7   | Console writes                          | (a) the range is changed only by declaration and deploy; (b) the console can override min/max live, recorded as drift.                                                                                                                                                                                                                                                                           | (a). Git stays the source of truth, which is forge's model; an agent changes the range with a one-line edit and a deploy.                                                                                                                                                                  | Slice 3 UI scope.                               |

## 9. Rollout plan

Each slice ships alone and leaves main correct.

0. **Verify, no code** (control-plane, read-only in prod by someone with
   access): metrics-server serves pod metrics for gVisor pods on
   `prod-daemon-v2` (the v2 shadow ledger's `cpu_used_milli` rows for hosted
   pods are the cheap evidence; `kubectl top pods` in a tenant namespace the
   direct one); the cluster's default StorageClass allows volume expansion;
   whether ManagedDatabase volumes count in the org storage pool. Any "no"
   becomes a slice-2/4 prerequisite.
1. **forge: schema, render, apply** (`feat(deploy): forge.Autoscale renders a
HorizontalPodAutoscaler`): the Go type and validation, tiers regeneration,
   the KCL field and masks, the renderer (HPA, no `spec.replicas`, PDB and
   spread on max), the handover in `internal/cluster`, the override refusal,
   the metrics-server check, the hosted capability refusal, positive and
   negative KCL tests, a render golden, and an e2e that deploys to k3d with
   metrics-server and watches the HPA scale. Self-hosted autoscaling works end
   to end after this slice; hosted refuses it with a clear message.
2. **control-plane: hosted HPA**: pin forge, kind set, handover, admission at
   the max, claimable refusal, lapse/resume, pending-pod and pool alerts, k3d
   metrics-server and the `e2e/hosted_deploy` scale test, then advertise
   `autoscales-workloads`.
3. **Status and console**: the additive status fields, `workloadhealth`
   states, `forge env status` / `obs workloads` / MCP output, the reliant web
   display and the cost range in `env shape`.
4. **Database storage autogrow**: the forge field and capability, the fill
   signal (decision 5) and its alert, the operator evaluator, explicit
   StorageClass, admission at `maxGiB`, activity events and the panel.
5. **Docs**: benefits limit narrowed, new benefit, compare row, skills, and the
   north star rows HO-01 and HO-02 marked shipped.

**Dogfood.** First, the self-hosted path on our own platform: control-plane's
registry (`deploy/kcl/lib/registry.k`), whose own comment asks for exactly this,
moves from `replicas = 2` to `autoscale = forge.Autoscale {minReplicas = 2,
maxReplicas = 4}` in prod after slice 1, exercising the handover on a live
two-replica Deployment. Second, the hosted path: the `e2e/hosted_deploy`
fixture runs autoscaled in every e2e, and the first reliant-labs app bound to
`forge.OnHosted` with real traffic opts in after slice 2. Third, one hosted
managed database with `storageAutogrow` filled deliberately in e2e past 80%,
watching the grow, the event and the bill row.

**Explicitly out of v1:** scale-to-zero; database compute autoscale; memory and
custom-metric HPAs; KEDA; VPA; traffic-split canaries; autogrow on self-hosted
clusters; console writes of the range; tunable autogrow threshold and step.
