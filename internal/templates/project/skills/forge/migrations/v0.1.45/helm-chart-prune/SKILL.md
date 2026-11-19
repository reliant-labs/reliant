---
name: migrations/v0.1.45/helm-chart-prune
description: Adopt the `forge.HelmChart` changes — a chart re-apply now PRUNES the objects the chart stopped rendering (behavior change), `namespace_labels` / `namespace_annotations` for the Namespace forge creates (Pod Security Admission on flux-system), and `KubeconfigPolicyRule.resource_names`. Use when an env declares a `forge.HelmChart` that ever dropped an object, when a chart's Namespace needs PSA labels, or when a minted KubeconfigSecret is paired with a hand-written ClusterRole only to restrict a rule to named objects.
---

# HelmChart: pruning, namespace metadata, `resource_names` (v0.1.45, section 11 of `migrations/v0.1.45`)

Use when an env declares a `forge.HelmChart` and a chart version ever dropped an object (Flux's ClusterRoles and NetworkPolicies are the usual one), when a chart's Namespace needs Pod Security Admission labels, or when a minted `KubeconfigSecret` is paired with a hand-written ClusterRole only to restrict a rule to named objects.

## What changed

- **Behavior change: a chart apply now prunes.** forge stamps every object a `forge.HelmChart` renders with `forge.dev/chart-owner=<project>.<env>` and, once the chart's apply has fully succeeded, deletes the objects carrying that stamp that the new render no longer contains. Before, an object a chart stopped rendering stayed live forever. Kept, never deleted: CRDs, Namespaces, PersistentVolumes, PersistentVolumeClaims, StatefulSets and Secrets; an object annotated `kustomize.toolkit.fluxcd.io/prune: disabled`; an object with `ownerReferences`; anything from a chart whose render is empty or unreadable. A `--target` apply of something other than the chart prunes nothing.
- **Current inventory safety:** chart pruning now requires UIDs recorded from successful SSA responses, a non-expiring per-chart lock in kube-system, and UID/resourceVersion-conditional deletes. The first inventory run deletes nothing. Previously dropped, untracked objects still need one-time manual deletion. Failed/cancelled commands retain their lock; see `deploy/flux` for the mandatory termination/fencing and explicit `forge cluster recover-chart-lock` procedure. Never clear a live writer's lock or infer death from its age.
- `forge.HelmChart` gained `namespace_labels`, `namespace_annotations` (written onto the Namespace forge creates for the chart) and `prune` (default `True`).
- `forge.KubeconfigPolicyRule` gained `resource_names`.

## Steps

1. Before the first deploy under this version, `forge env render <env> --target <chart>` and read what the chart renders: that is what survives the prune.
2. Only forge writes the `forge.dev/chart-owner` label, so a hand-made object in the chart's namespace is never a candidate. To make a rendered object outlive its chart anyway, annotate the live object `kustomize.toolkit.fluxcd.io/prune=disabled`.
3. A chart whose live objects are hand-managed: `prune = False` on the `forge.HelmChart`.
4. PSA on a chart's namespace:

   ```python
   forge.flux_chart() | {
       namespace_labels = {"pod-security.kubernetes.io/enforce" = "restricted"}
   }
   ```

   Two envs that install the same chart into one cluster must declare the same metadata, or each re-apply strips what the other set.
5. A hand-written ClusterRole that exists only to grant `bind` (or `get`) on named objects beside a mint: delete it and put `resource_names = [...]` on the `forge.KubeconfigPolicyRule`.

## Verify

`forge env deploy <env> --target <chart>` prints `Pruning N object(s) chart "<chart>" no longer renders` when it deleted something and `keeping it (<reason>)` for each departed object a guard protected. `kubectl get ns <ns> --show-labels` shows the declared labels.
