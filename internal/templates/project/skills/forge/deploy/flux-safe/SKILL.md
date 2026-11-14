---
name: deploy/flux-safe
description: What makes an object wrong only under a reconciler (Flux) — a Job with ttlSecondsAfterFinished, a Job with no spec hash in its name, force on a kind that holds data — the `flux-safe` lint, what the bundle build fixes for you, and the per-tree budget it records.
---

# Flux-safe bundles

`forge env deploy` applies once and exits. Flux re-applies the same bytes on every
interval and prunes what the bundle stops carrying, so some objects that are fine
under `kubectl apply` are wrong under it. `forge lint` (`flux-safe`) and the bundle
build check them.

## Refused (you must fix it)

| Rule | Why it is wrong under Flux | Fix |
|---|---|---|
| `flux-job-ttl` — `ttlSecondsAfterFinished` on a Job | the TTL controller deletes the finished Job, the next reconcile creates it again: a migration re-runs every period | remove it. A superseded Job goes away by its spec-hash name plus prune, never by a timer (Flux's inventory prune; a direct apply keeps the newest 3 finished Jobs of each name and deletes the older ones after the apply) |
| `flux-job-unhashed` — a Job name not ending in `-<6+ hex>` | a Job's spec is immutable, so after any change every reconcile fails instead of creating a new Job | name it `<name>-<hash of its spec>`; a forge `kind = "job"` workload already is |
| `flux-force-on-data` — `kustomize.toolkit.fluxcd.io/force: enabled` on a PVC, PV, StatefulSet, Secret, Namespace or CRD | Flux would delete and re-create it on an immutable-field change, losing its data | remove it; change the field through a migration |

These are **errors** for an object in a delivery unit a reconciler applies and
**warnings** for one forge applies itself, so a build that works today keeps
working. That is the whole env when it declares no `lifecycle`, no `applier` and no
`hub_converges`; only the listed units when it declares `hub_converges`; and
nothing when it declares `lifecycle = "local"`/`"ephemeral"`. A warning is the
to-do list for the move onto Flux: fix it before listing the unit in
`hub_converges`, which is when it becomes an error.

## Done for you (nothing to change)

- A Deployment's `strategy` (`RollingUpdate`, 25%/25%) and `progressDeadlineSeconds`
  (600) are rendered explicitly, for forge workloads and for a Deployment you wrote
  in `forge.Manifests`. Values you wrote are kept; `Recreate` never gains
  `rollingUpdate`.
- A `StorageClass`, `RuntimeClass`, `PriorityClass`, `ClusterRoleBinding` or
  `RoleBinding` gets `kustomize.toolkit.fluxcd.io/force: enabled`, so Flux can
  recreate it on an immutable-field change as direct apply does. A `Service`, a
  Deployment and a Job are not stamped (a Service releases its LoadBalancer
  address); annotate those yourself if you mean it.

- A StatefulSet with `volumeClaimTemplates` gets a pre-rollout **resize Job** (a
  hash-named, TTL-free Job in the unit's `pre` tree, so it passes the rules above) that
  grows its claims when the declared size grew. Flux dry-runs every object of a tree
  before applying any, so without it one raised size would wedge the whole unit:
  `deploy/statefulset-volumes`.

## Budgets

Each tree in the bundle records `budget_seconds`: the largest `progressDeadlineSeconds`
(rollout trees) or `activeDeadlineSeconds` (Job trees, 600 s if unstated) among the
objects it is judged on, plus 120 s. Flux itself only waits one short attempt; the
control plane fails a tree when this budget is spent. Declare `activeDeadlineSeconds`
on a slow migration so its tree is given the time it needs.
