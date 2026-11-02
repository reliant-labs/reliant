---
name: deploy/plan
description: Reading the deploy plan `forge env deploy` prints and `--approve` binds — what it diffs against (the last succeeded apply, recorded by env and bundle digest), who computes it (the control plane or forge), object added/removed/changed with field paths, secret presence read from the cluster, and what `no recorded config to compare against` means.
---

# The deploy plan

`forge env deploy <env>` prints a plan before it writes anything, and
`--approve <digest>` binds the deploy to exactly that plan. The plan's first
line says what it was computed against:

```
Plan for env prod (release 20261010.065439-c37e9ec37315)
  computed against: promotion c7539113-…, applied bundle 8e896e17-c6c
```

## What Live is: the last SUCCEEDED apply

The plan diffs the bundle about to ship against the bundle of the env's
newest **succeeded** apply — never the declared shape (`forge env build`
refreshes that from the very render being deployed, so diffing against it
would hide every change). A failed apply newer than the last good one is not
what runs, so it is skipped. A pointer move nothing applied is skipped too.

Every successful apply records the bundle it shipped, keyed by env and bundle
digest:

| env | recorded where | read by |
|---|---|---|
| file ledger (no `forge.ControlPlane`) | the machine ledger's apply record | forge |
| `forge.ControlPlane`, converges nothing (control-plane prod) | the promotion's `apply` gate, `details.bundle_digest` (+ `plan_digest`) | forge |
| hosted tiers, or a connected cluster | the control plane's own deployments / Flux observations | the control plane |

`no recorded config to compare against: every object appears added` means no
identifiable apply exists yet — a first deploy, one whose newest succeeded
apply was recorded before the gate carried a bundle digest, or a scoped
(`--target`, `--frontends-only`) apply, which shipped only part of its bundle
and so names none. The walk stops there rather than diffing against something
older that no longer runs, so the next successful whole-env deploy records the
baseline and the plan after it is clean.

## Who computes it

**Whoever applies the env**, because only the applier can see Live.

- **The control plane**, for an env it converges. The digest travels on the
  promotion write and the server recomputes the plan under the env row lock;
  a mismatch is `plan_stale` (exit 3).
- **forge**, for an env forge applies — a file-ledger env, and a
  `forge.ControlPlane` env whose `converges` line says `nothing`. Its control
  plane observes nothing of that cluster, so it could only ever plan against
  an empty Live. forge recomputes the plan right before the write and refuses
  any other `--approve` digest (exit 3); the digest is not sent for the
  server to recompute, and is recorded on the apply gate instead.

## Findings

| finding | class | meaning |
|---|---|---|
| `object_added` | info | in the candidate, not in the last applied bundle |
| `object_removed` | warn | applied last time, gone from the render now. A reconciler (Flux, the control plane) prunes it; forge's direct apply prunes only what it can prove is its own (see **Owned-object prune** below): an inert object it stamped `forge.dev/owner`, listed as `[prune]` in the plan and deleted once the rollout is healthy. Everything else it does NOT delete (Deployments need `--prune`; a `forge.HelmChart`'s own objects are pruned after the chart's apply), so the detail says so — if it still exists, delete it by hand; the next plan no longer lists it. A hash-named one-shot **Job** is the exception to the exception: every release that changes its spec reports the previous Job as removed, and the direct apply deletes finished superseded Jobs itself (the newest 3 of each name stay), so its detail says that instead |
| `stateful_deletion` / `lb_identity_change` | **stop** | a removal that loses data or an external address; needs `--acknowledge-destructive <code>` even with `--yes` |
| `config_changed` | info | the object's non-image fields changed; detail names them: `fields: spec.replicas, spec.template.spec.containers[api].env[LOG_LEVEL].value` |
| `object_changed` | info | changed, where the image/config split cannot be made |
| `image_changed` | info | a release-bound image digest moved |
| `secret_needed` | info / warn | see below |
| `unknown` | warn | a section that could not be computed — never read it as "no changes" |

Field paths name list items by `name` (containers, env vars, ports) and never
carry a value — the plan is printed into CI logs. They are prose: a finding's
detail is not part of the digest, so two machines that could and could not
fetch the bundles approve the same plan.

## Secret presence

- `external` — read from the target cluster through the env's kubectl
  context, **key names only** (a go-template that prints keys; no value ever
  reaches forge). Checked where each Secret is read: the workload's context
  and namespace, or, for a declared `forge.ExternalSecret` no workload reads,
  any of the env's clusters.
- `hosted` — read from the control plane's secret store (names only).

| state | finding |
|---|---|
| declared last time, still present | none |
| newly declared, present | `info` |
| MISSING — new, or deleted since the last apply | `warn`, and the deploy's preflight refuses it before anything is recorded |
| could not be read (no credential, cluster unreachable) | none if declared before; `warn … presence not verifiable` if new — never "missing" |

## Drift

For an env forge applies, drift is reported as `live drift is not observable
for this environment`: nothing compares the live objects with the applied
bundle yet. `forge env deploy <env> <version> --live-diff` runs a client-side
`kubectl diff` of the release's bundle against the cluster when you need to
see it.

## What a release and a promotion record

**A scoped deploy records what it moved.** The promotion carries `targets`
(the `--target` names, or the env's shippable frontends for
`--frontends-only`), so the ledger never claims the whole env runs a release
only part of it took; `release.Promotion.Moved(component)` answers per
workload, and the plan prints `scope` and `scoped: only <x> moved`.
Re-deploying the bound release scoped records nothing new; a whole deploy
after a scoped one is a real move (`COMPLETES A SCOPED DEPLOY`). Where the
env's control plane converges promotions (a hub, or hosted tiers) a scoped
deploy of a NEW release is refused before anything is written: the control
plane would move more than the scope names.

**A release records its source history.** The cut stores its commit's
subject and author (`provenance.commit_subject` / `commit_author`) and the
commits it adds over the newest earlier release it descends from (`commits`:
at most 100, with `truncated`, and a `total` that is 0 when a shallow clone
could not count them). Every bundle carries a one-line summary and
per-section digests for each object (`objects`). `release.CompareCommits`,
`CompareArtifacts` and `CompareObjects` say what changed between two
releases with no repository or manifests at hand; a control plane serves
the same answer as `DeployService.CompareReleases`.

## Owned-object prune (direct apply)

`kubectl apply` adds and updates; nothing reconciles the set. A release that
REPLACES an object (the SC7 OpenBao adoption swapped a bespoke store for
`forge.OpenBao`) used to leave the old one behind: the previous PodDisruptionBudget
(two budgets over one set of pods block every node drain), NetworkPolicy, RBAC and
auth-delegator ClusterRoleBinding all had to be deleted by hand.

Every object a direct apply sends is stamped `forge.dev/owner=<project>.<env>`
(a label only: no selector, no pod template, so nothing rolls; Secrets and CRDs
are not stamped, CRDs have `forge.dev/crd-owner`). After a **full** apply whose
rollout came up healthy, forge deletes the objects that

1. carry that label (the listing is selected server-side, so an object forge did
   not stamp, or another env did, is never even a candidate),
2. are absent from the render for that cluster (read **before** the hub's delivery
   units are taken out, so a hub-converged object is never mistaken for a removed
   one),
3. are an **inert kind**: ServiceAccount, Role, RoleBinding, ClusterRole,
   ClusterRoleBinding, NetworkPolicy, PodDisruptionBudget, ConfigMap, Service
   (not a LoadBalancer), Ingress, HorizontalPodAutoscaler,
4. do not carry `kustomize.toolkit.fluxcd.io/prune: disabled`, have no
   `ownerReferences`, and are not already terminating.

The delete is made against the UID and resourceVersion just read. Never touched:
Deployments (`--prune`), StatefulSets, PVCs, Secrets, ExternalSecrets, Namespaces,
CRDs, Jobs (their own housekeeping), LoadBalancer Services. More than 50 removals
in one deploy is refused wholesale (a wrong render, not a smaller env), as is an
empty render. A `--target` deploy, a rollout mode other than `wait`, and a failed
rollout prune nothing. A failed prune is a warning, never a failed deploy.

`forge env deploy <env> --plan-only` (and `--dry-run`) lists exactly this set as
`object_removed` findings marked `[prune]`, digested, so approving the plan
approves those deletions and no others. A cluster the plan could not read is an
`unknown` warn naming it. It is on by default. To keep one object, annotate it
`kustomize.toolkit.fluxcd.io/prune: disabled` (the same guard Flux honors): the
plan then lists it as kept, with that reason, instead of deleted.

**Objects applied before the stamp existed carry no label**, so the first deploy
after upgrading forge cannot prune them. To hand one over, label it once:
`kubectl label <kind> <name> -n <ns> forge.dev/owner=<project>.<env>`; the next
deploy deletes it if the render does not carry it.
