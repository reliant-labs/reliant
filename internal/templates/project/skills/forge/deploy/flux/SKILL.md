---
name: deploy/flux
description: Flux in forge — what forge no longer does (install Flux into a deploy target, write a desired-state pointer at one), who applies a real env instead, and the one thing that remains (`forge.flux_chart()`, the pinned chart a consumer declares when it runs a Flux of its own). Use when a skill or an old note says an env without a control plane reconciles through an in-cluster Flux.
---

# Flux

**forge never installs Flux into a deploy target and never writes a
desired-state pointer (OCIRepository + Kustomization) at one.** An earlier
design did both for an env with no control plane; #461 removed it and the code
has since been deleted. If a note says `forge cluster up` installs
`source-controller`/`kustomize-controller`, or that `forge env deploy` "writes the
pointer and waits", it is describing a path that does not exist.

## Who applies a real environment

| the env declares | applied by |
|---|---|
| `forge.ControlPlane` and clusters bound to connected clusters (`forge cluster connect`) | the control plane's hub Flux converges the recorded bundle; forge records and waits (`deploy/cluster-connect`) |
| `hub_converges = [units]` | the hub converges **those delivery units** from the recorded bundle; forge applies the rest directly. This is how a real env moves onto Flux one unit at a time (`deploy/shape`) |
| `applier = "forge"` | **forge**, directly, for now: no unit has moved to the hub yet. A transitional state, the same as `hub_converges = []`; every real env ends on the hub (`deploy/shape`) |
| `lifecycle = "local"` / `"ephemeral"` | forge, directly: the clusters are scratch it creates and deletes |
| none of these | forge, directly, with one notice that this will be refused (F-REFUSE-DIRECT) once an env must say which of the above it is |

Direct apply is where every env starts and where only dev and ephemeral envs end.
For a real env it is a stage, not a mode: list its units in `hub_converges` as
they move, and the declaration ends when the list is complete.

A Flux on a cluster exists only because a control plane declared one. Flux is a
platform dependency of the control plane, not something forge puts in your
cluster as a side effect.

## What remains: `forge.flux_chart()`

An env that needs Flux for its own reasons (control-plane's e2e runs one to
reconcile hosted workloads) declares forge's pinned chart as an ordinary bootstrap
`forge.HelmChart`:

```kcl
helm_charts = [
    forge.flux_chart(values = {
        # Merged OVER forge's posture, last-wins.
        sourceController.serviceAccount.annotations = {
            "iam.gke.io/gcp-service-account" = "flux@proj.iam.gserviceaccount.com"
        }
    })
    # Multi-cluster: re-target it like any other chart.
    # forge.flux_chart(cluster = _control_plane)
]
```

`forge cluster up <env>` installs the env's declared bootstrap charts (Flux
first, then charts that ship a CRD bundle, then the rest) into the env's
forge-managed k3d clusters; `forge env deploy <env> --target <chart>` installs
just that one.

The chart is **version-pinned with no knob**, and it runs **two controllers, not
six** (`source-controller`, `kustomize-controller`; helm, notification and the two
image controllers are off, a security decision rather than a resource one).

### Hardened and sized by default

A Flux that reconciles bundles for more than one author is a shared control
point, so the defaults are the hardened ones. Everything is overridable.

| Default | What it does | Turn it off / change it |
|---|---|---|
| `rbac = "namespaced"` | Flux CRs cluster-wide; leader election in `flux-system`; impersonation (pinned by `resourceNames` to one ServiceAccount) and Secret reads only where a RoleBinding names `flux-tenant-reconciler` / `flux-tenant-source`. Multitenancy on, no cluster-admin binding | `rbac = "pinned"` (cluster-wide reads, impersonation pinned), `rbac = "chart"` (the chart's own), `impersonate_service_account` |
| `flux_namespace_bindings(ns)` | the two RoleBindings a namespace holding Flux objects needs | declare per namespace |
| `DisableConfigWatchers` | config watcher off (it needs a cluster-wide Secret list) | `feature_gates = {DisableConfigWatchers = False}` with `rbac = "pinned"` |
| `CancelHealthCheckOnNewRevision` | a fix-forward revision cancels a stuck health check | `feature_gates = {...}` |
| `concurrency = 16`, `concurrency_ssa = 8`, `source_concurrency = 8`, `--requeue-dependency=10s` | one 13-minute rollout holds 1 of 4 default workers; this keeps a slow rollout from blocking everyone | the three arguments; or `values` |
| requests/limits, `priority_class_name = "forge-delivery"` | sized starting points; the PriorityClass is declared with the chart | `values`; `priority_class_create = False` to name a class that exists |
| NetworkPolicies | `allow-egress` only: **ingress to every `flux-system` pod only from `flux-system` pods** (an invariant, tested); no `allow-webhooks`; `allow-scraping` only for `scrape_namespaces` | `scrape_namespaces = ["observability"]`; `network_policies = "chart"` |
| `pod_security = "restricted"` | Pod Security enforce/warn/audit on `flux-system` | `pod_security = "baseline"` or `""` |

Opt-in placement, for a dedicated delivery node pool (forge has no node-pool
primitive, so provision it yourself, tainted `reliant.dev/pool=delivery-v1:NoSchedule`):

```kcl
forge.flux_chart(
    placement = forge.flux_placement_delivery_v1(
        runtime_class_name = forge.FLUX_RUNTIME_CLASS_GVISOR,   # only where the target has it
    )
)
```

`runtime_class_name` is never defaulted on: a pod naming a RuntimeClass the
cluster lacks never schedules. The chart has no `runtimeClassName` value, so it
reaches the pods through `HelmChart.patches` (`"Deployment/kustomize-controller" = {...}`),
which fails the render, naming the candidates, if a chart bump renames a Deployment.
The same field patches any chart's rendered objects.

**gVisor.** GKE Sandbox's documented gVisor limits (no privileged containers,
no hostPath, no port-forward, seccomp/AppArmor unsupported) are not features the
two controllers use: they run non-root with a read-only root filesystem and
`/tmp` / `/data` emptyDirs, listen on 8080/9090/9440 and call the API server.
Compatibility has not been run under a real sandbox here: verify on the target
(`kubectl -n flux-system get pods -o jsonpath='{.items[*].spec.runtimeClassName}'`,
a reconcile to a new revision, and `kubectl top`) before relying on it, and fall
back to the pool alone if it fails. Patching and CVE watch:
`docs/runbooks/flux-patch-and-upgrade.md`.

> **Chart version ≠ Flux version.** There is no fluxcd-published chart; forge
> uses the community chart at `oci://ghcr.io/fluxcd-community/charts/flux2`, whose
> version has its own lineage. Chart **2.19.1** is Flux **v2.9.5**. On a bump,
> re-derive it, do not increment:
> `helm show chart oci://ghcr.io/fluxcd-community/charts/flux2 --version <v> | grep appVersion`

### Namespace labels, and what a re-apply prunes

`flux-system` is created by forge (`helm template` never emits a chart's
Namespace), so its metadata is declared on the chart:

```kcl
forge.flux_chart() | {
    namespace_labels = {
        "pod-security.kubernetes.io/enforce" = "restricted"
        "pod-security.kubernetes.io/enforce-version" = "latest"
    }
}
```

They converge under server-side apply: a changed value is updated, a key you
stop declaring is removed, a key another manager set is left alone.

The labels forge stamps on that Namespace are forge's, and declaring one is
refused: every `forge.dev/*` key, `app.kubernetes.io/name` (the chart's `name`),
`app.kubernetes.io/managed-by` (`forge`), and the apiserver's
`kubernetes.io/metadata.name`. Every other key, PSA included, is yours.

A re-apply **deletes objects the chart stopped rendering**, but only UIDs an
earlier successful apply recorded. Inventory ConfigMap
`kube-system/forge-chart-inventory-<hash>` is keyed by project/env owner and
chart (moving target namespaces cannot fork it). UIDs come from SSA responses,
not a later name lookup. The live UID and ownership stamp must still match.
Deletion sends UID **and resourceVersion** preconditions, so a concurrent
replacement, prune annotation or ownership change is protected.

CRDs, Namespaces, PV/PVC, StatefulSets and Secrets are never deleted or queried
for prune. `kustomize.toolkit.fluxcd.io/prune: disabled` and `ownerReferences`
keep an object. Failed applies leave inventory unchanged and prune nothing;
partial writes remain conservatively untracked until a successful retry.
The first run without an inventory deletes nothing. Previously dropped objects
still need manual deletion once. `prune = False` disables deletion, not locking
or inventory tracking. Prune/inventory errors fail the deploy, not just warn.

### Chart lock and explicit crash recovery

Every owned chart apply first atomically creates a non-expiring ConfigMap lock
`kube-system/forge-chart-lock-<hash>`, holding it through apply, inventory commit
and prune. Another applier fails before chart writes. The applier requires
ConfigMap get/create/update/delete in kube-system. There is no automatic expiry,
stealing, `--force`, or age-based unlock. This intentionally trades automatic
recovery for protection against a suspended writer resuming after lease expiry.

A clean successful apply conditionally releases its own lock. A failed or
cancelled command retains it: an API write may still be in flight. Recovery is
an operator action, **never a deploy retry loop**:

1. Read the lock named in the error with an explicit kubectl context. Inspect
   its owner/chart labels, `data.holder`, `data.started`, metadata UID and
   resourceVersion. Check the originating host/process and deployment logs.
2. **Terminate or fence that holder's forge process AND its kubectl children on
   every host.** A suspended process is not dead. Resolve outstanding API writes
   or revoke their credential and establish they cannot complete later. Do not
   recover merely because a process is slow or a timestamp is old. If you cannot
   prove the old writer cannot resume, leave the lock and investigate.
3. Only then explicitly attest the inspected holder (the CLI does not claim to
   prove remote process termination):

   ```sh
   forge cluster recover-chart-lock --context <explicit-context> \
     --owner <project.env> --chart <chart-name> \
     --uid <inspected-lock-uid> --resource-version <inspected-lock-rv> \
     --confirm-terminated 'terminated:<inspected-data.holder>'
   ```

   The command refuses missing/mismatched confirmation and conditionally deletes
   only that exact lock UID/resourceVersion. It never deletes the inventory.
4. Re-run the declarative apply. Old/untracked objects need inspection; never
   manufacture inventory entries or remove a lock while its writer is live.

Detection of "this env already provides Flux" is **by chart reference, never by
name**: a chart's `name` is the `--target` selector its author picks freely, so a
name heuristic would let a naming choice decide whether a colliding second Flux
was installed. Declare `forge.FLUX_CHART_OCI` and you are recognised, whatever you
call the chart.
