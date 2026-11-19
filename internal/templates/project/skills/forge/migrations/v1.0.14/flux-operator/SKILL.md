---
name: migrations/v1.0.14/flux-operator
description: Adopt `forge.flux_chart()` installing Flux through ControlPlane's Flux Operator and a FluxInstance (behavior change) instead of the community flux2 chart — the chart is renamed `flux-operator`, `values` now reaches the operator chart, the `flux_check` egress key became `operator`, `rbac`/`network_policies = "chart"` became `"upstream"`, and an existing install is adopted in place. Use when deploy/kcl declares `forge.flux_chart(`, names the chart `flux`, passes `values.extraObjects` / `values.kustomizeController` / `egress.flux_check`, or targets `--target flux`.
---

# `forge.flux_chart()` is the Flux Operator + a FluxInstance (v1.0.14)

## What changed

`forge.flux_chart()` no longer renders the community `flux2` chart. It renders
ControlPlane's [Flux Operator](https://github.com/controlplaneio-fluxcd/flux-operator)
chart (pinned, image by digest) with a hardened FluxInstance riding it; the
operator installs source-controller and kustomize-controller (Flux 2.9) from the
manifests embedded in its image. The posture is the same (per-namespace RBAC,
multitenancy, sizing, PSA restricted, forge's NetworkPolicies, the connected
shard); `forge skill load deploy/flux` describes it. What a consumer edits:

| before | now |
|---|---|
| chart name `flux` (`--target flux`) | `flux-operator` (`--target flux-operator`) |
| `values` → flux2 chart values | `values` → Flux Operator chart values |
| `values.extraObjects` | `extra_objects = [...]` |
| `values.kustomizeController.*`, `.sourceController.*` | `instance_patches = [...]` (FluxInstance `kustomize.patches`, applied after forge's) |
| `values.imagePullSecrets` for Flux images | `distribution = {imagePullSecret = "..."}` (a mirror: `registry`, `variant`, `artifact`) |
| `rbac = "chart"`, `network_policies = "chart"` | `"upstream"` |
| `egress = {flux_check = [...]}` | `egress = {operator = [...]}` — the operator needs the API server (and a mirror artifact's registry) |
| shard Deployment rendered by forge | the FluxInstance shard `connected`, on its own ServiceAccount |

New: `resourceset_service_account` (default `flux-delivery`: what a ResourceSet
with no `serviceAccountName` impersonates) and `override_managers` (the
operator's `--override-manager`). New admission policies ride the chart:
`flux-controller-bindings` and `flux-connected-shard-identity`.

## Steps

1. Rename any reference to the chart `flux` (a `--target`, an override, a
   `| {name = ...}`) and port the `values` keys per the table.
2. **Do not delete or re-render the old chart.** It is a different chart name,
   so forge stops applying it and never prunes it; the operator adopts its
   Deployments, ServiceAccounts, Service and CRDs in place (same names and
   selectors; it takes ownership of fields forge's `kubectl` apply wrote). No
   CRD is deleted and no Kustomization is pruned.
3. `forge env deploy <env> --target flux-operator`. Riding NetworkPolicies,
   RBAC, ServiceAccounts and PriorityClasses now land before forge waits for the
   chart's Deployments, so an operator starting under a default-deny
   `flux-system` gets its egress allow first.
4. Verify: `kubectl -n flux-system get fluxinstance flux` is Ready; the
   controllers run the operator's images with forge's flags
   (`kubectl -n flux-system get deploy -o yaml | grep -A14 ' args:'`); no
   `cluster-reconciler-flux-system` binding exists; every Kustomization is still
   Ready at its revision.
5. Delete the old chart's leftovers: Job and ServiceAccount `flux-flux-check`,
   NetworkPolicy `flux-check-egress`, ClusterRoles `flux-edit` / `flux-view`
   (the operator renders `-flux-system` copies), and the `flux` chart's
   `forge-chart-inventory-*` / `forge-chart-lock-*` ConfigMaps in `kube-system`
   (label `forge.dev/chart-inventory=flux` / `forge.dev/chart-lock=flux`).

**Never delete the FluxInstance** to undo this: the operator uninstalls Flux on
deletion (forge keeps the CRDs, but the controllers go). Pause it with the
annotation `fluxcd.controlplane.io/reconcile=disabled`, then redeploy the
previous forge pin.
