---
name: deploy/flux
description: Flux in forge — who applies a real env, the opt-in self-hosted delivery (`applier = "flux"`, see deploy/flux/self-hosted), and `forge.flux_chart()` (ControlPlane's Flux Operator plus a hardened FluxInstance, pinned) for a consumer that runs a Flux of its own, such as a control plane's hub. Use when declaring or tuning a Flux install, or deciding who applies an env.
---

# Flux

**forge installs Flux into a deploy target only when the env opts in with
`applier = "flux"`** (below). Every other env is unaffected: `forge cluster up`
installs no Flux of its own, and `forge env deploy` writes no desired-state
pointer at a cluster. Only an env that declares `applier = "flux"` is ever
delivered that way.

## Who applies a real environment

| the env declares | applied by |
|---|---|
| `forge.ControlPlane` and clusters bound to connected clusters (`forge cluster connect`) | the control plane's hub Flux converges the recorded bundle; forge records and waits (`deploy/cluster-connect`) |
| `hub_converges = [units]` | the hub converges **those delivery units** from the recorded bundle; forge applies the rest directly. This is how a real env moves onto Flux one unit at a time (`deploy/shape`) |
| `applier = "forge"` | **forge**, directly, for now: no unit has moved to the hub yet. A transitional state, the same as `hub_converges = []`; every real env ends on the hub (`deploy/shape`) |
| `applier = "flux"` (no control plane) | a Flux **forge installs in the env's own cluster**, reading the env's bundle; forge never applies the bundle itself (below) |
| `lifecycle = "local"` / `"ephemeral"` | forge, directly: the clusters are scratch it creates and deletes |
| none of these | forge, directly, with one notice that this will be refused (F-REFUSE-DIRECT) once an env must say which of the above it is |

Direct apply is where every env starts and where only dev and ephemeral envs end.
For a real env it is a stage, not a mode: list its units in `hub_converges` as
they move, and the declaration ends when the list is complete.

A Flux on a cluster exists only because something declared one: a control
plane, or an env that opted into `applier = "flux"`. Flux is a declared platform
dependency, never something forge puts in your cluster as a side effect.

## A self-hosted env reconciled by its own Flux: `applier = "flux"`

An env with **no control plane** can opt in to being reconciled by a Flux forge
installs in its own cluster: declare `applier = "flux"` on the Bundle, run
`forge env deploy <env> --target flux-delivery` once, then ship releases with
the ordinary `forge env deploy <env> [<version>]`, which pushes the bundle and
points that Flux at it. Load `deploy/flux/self-hosted` for the declaration, the
bootstrap, shipping, and what happens when a release needs a permission the
Flux was not granted.

## What remains: `forge.flux_chart()`

An env that needs Flux for its own reasons (a control plane's hub reconciling
hosted workloads, or a self-hosted cluster pulling its desired state) declares
forge's pinned install as an ordinary bootstrap `forge.HelmChart`:

```kcl
helm_charts = [
    forge.flux_chart()
    # Multi-cluster: re-target it like any other chart.
    # forge.flux_chart(cluster = _control_plane)
]
```

What that installs, in two pinned layers (credit: the
[Flux Operator](https://github.com/controlplaneio-fluxcd/flux-operator) by ControlPlane):

1. **The Flux Operator**, chart `oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator`
   at `FLUX_CHART_VERSION`, image pinned **by digest**. Its web UI and its
   aggregation into admin/edit are off. Its FluxInstance, FluxReport,
   ResourceSet and ResourceSetInputProvider CRDs and controllers are all on; a
   ResourceSet with no `serviceAccountName` impersonates
   `resourceset_service_account` (default `flux-delivery`) in its own namespace
   instead of running as the operator. `override_managers` adds
   `--override-manager` flags: the field managers whose ownership the operator
   (a ResourceSet adopting objects, or the FluxInstance) takes over.
2. **A FluxInstance** named `flux`, riding the chart. The operator renders Flux
   from manifests **embedded in its own image**: `FLUX_VERSION` pins the minor
   (`2.9.x`), the patch is the newest one the pinned operator embeds, and no
   `distribution.artifact` is set, so nothing third-party changes what runs.
   `distribution = {registry, variant, imagePullSecret, artifact, artifactPullSecret}`
   points at a mirror (a mirrored `flux-operator-manifests` artifact ships Flux
   CVE patches without an operator bump); `version` is refused.

`values` merges over the operator chart's values; `instance_patches` appends
FluxInstance `kustomize.patches` after forge's; `extra_objects` appends riding
objects. **Deleting a FluxInstance uninstalls Flux**, CRDs included, so forge
marks every CRD `fluxcd.controlplane.io/prune: disabled` and the FluxInstance
`kustomize.toolkit.fluxcd.io/prune: disabled`; pause the operator with the
annotation `fluxcd.controlplane.io/reconcile: disabled` instead.

`forge cluster up <env>` installs the env's declared bootstrap charts (Flux
first, then charts that ship a CRD bundle, then the rest) into the env's
forge-managed k3d clusters; `forge env deploy <env> --target <chart>` installs
just that one.

The install is **version-pinned with no knob**, and it runs **two controllers, not
six** (`components: [source-controller, kustomize-controller]`; helm,
notification, the two image controllers and source-watcher are not installed, a
security decision rather than a resource one).

**The controller flags are replaced, not appended to.** Each controller's
`args` is one FluxInstance patch with exactly the flags below. The operator's
sharding profile otherwise appends `--watch-label-selector=!sharding.fluxcd.io/key`
to every main controller, which orphans any object carrying a shard value
(control-plane labels hosted objects `shard0`).

### Hardened and sized by default

A Flux that reconciles bundles for more than one author is a shared control
point, so the defaults are the hardened ones. Everything is overridable.

| Default | What it does | Turn it off / change it |
|---|---|---|
| `rbac = "namespaced"` | the distribution's `cluster-reconciler-flux-system` (cluster-admin) and `crd-controller-flux-system` are deleted from the build; Flux CRs cluster-wide; leader election in `flux-system`; impersonation (pinned by `resourceNames` to one ServiceAccount) and Secret reads only where a RoleBinding names `flux-tenant-reconciler` / `flux-tenant-source`. `multitenant` on. The `flux-controller-bindings` admission policy refuses any other binding of a controller ServiceAccount, so an upstream rename fails the operator's apply instead of granting cluster-admin | `rbac = "pinned"` (cluster-wide reads, impersonation pinned), `rbac = "upstream"` (the distribution's own, no lockdown), `impersonate_service_account` |
| `flux_namespace_bindings(ns)` | the two RoleBindings a namespace holding Flux objects needs | declare per namespace |
| `DisableConfigWatchers` | config watcher off (it needs a cluster-wide Secret list) | `feature_gates = {DisableConfigWatchers = False}` with `rbac = "pinned"` |
| `CancelHealthCheckOnNewRevision` | a fix-forward revision cancels a stuck health check | `feature_gates = {...}` |
| `concurrency = 16`, `concurrency_ssa = 8`, `source_concurrency = 8`, `--requeue-dependency=10s` | one 13-minute rollout holds 1 of 4 default workers; this keeps a slow rollout from blocking everyone | the three arguments |
| requests/limits, `priority_class_name = "forge-delivery"`, `enableServiceLinks: false` | sized starting points on every Flux pod; the PriorityClass rides the chart | `instance_patches`; `priority_class_create = False` to name a class that exists, `""` for the distribution's own |
| NetworkPolicies | the FluxInstance's `networkPolicy` is **off** and forge declares them (a patch that stopped matching could never quietly restore the stock ones): `allow-egress` only — **ingress to every `flux-system` pod only from `flux-system` pods** (an invariant, tested); egress open (`[{}]`) unless you declare `egress`; no `allow-webhooks`; `allow-scraping` only for `scrape_namespaces` | `egress = {...}` (below); `scrape_namespaces = ["observability"]`; `network_policies = "upstream"` (the FluxInstance's stock set) |
| `pod_security = "restricted"` | Pod Security enforce/warn/audit on `flux-system`, written on the Namespace forge creates AND patched into the one the operator renders (it takes the Namespace over and would strip the label) | `pod_security = "baseline"` or `""` |

### Declare the hub's egress

The default leaves every Flux pod's egress open, exactly as Flux ships it. A hub that reconciles other people's bundles should not run that way:
kustomize-controller dials whatever address a Kustomization's kubeconfig names,
so open egress lets that address be anywhere. `egress` declares it:

```kcl
_dns = {"to" = [{namespaceSelector.matchLabels = {"kubernetes.io/metadata.name" = "kube-system"}, podSelector.matchLabels = {"k8s-app" = "kube-dns"}}],
        ports = [{port = 53, protocol = "UDP"}, {port = 53, protocol = "TCP"}]}
forge.flux_chart(egress = {
    kustomize = [_dns, {"to" = [{podSelector.matchLabels = {app = "source-controller"}}], ports = [{port = 9090, protocol = "TCP"}]},
                 {"to" = [{ipBlock.cidr = "172.16.0.0/28"}], ports = [{port = 443, protocol = "TCP"}]}]   # the hub's API
    source = [_dns, ...]
    operator = [_dns, {"to" = [{ipBlock.cidr = "172.16.0.0/28"}], ports = [{port = 443, protocol = "TCP"}]}]   # the API server (+ a mirror artifact's registry)
})
```

- Keys: `kustomize`, `source`, `operator` (the Flux Operator, selected by
  `app.kubernetes.io/name: flux-operator`), and `kustomize_connected` (only
  with `connected_shard`). A key **replaces** that pod's rules.
- Riding NetworkPolicies, RBAC, ServiceAccounts and PriorityClasses are applied
  WITH the chart, before forge waits for its Deployments: an operator starting in
  a `flux-system` whose egress is already default-deny cannot become Available
  until its allow exists.
- Once `egress` is set, **every** controller must be named. An unnamed one would
  silently lose all egress, so the render refuses; `[{}]` opts one back into
  open egress on purpose.
- Why it replaces rather than adds: **NetworkPolicy allows are additive.** A
  policy added beside `allow-egress` with `egress: [{}]` is cancelled by it. With
  `egress` set, `allow-egress` keeps only its ingress rule (the invariant above)
  and selects every pod for Egress with no rule, i.e. default deny; each
  controller gets `<controller>-egress`, selected by its pod label. The
  per-controller policies render before `allow-egress`, so re-applying over a
  live open policy never has a window with neither.
- Leave the metadata server out of `kustomize` once its Workload Identity is
  gone; put `169.254.169.254/32` on `source` if it pulls from Artifact Registry.

### The connected-cluster shard (`connected_shard = True`)

Applying to a cluster someone else owns (`spec.kubeConfig`) is a different
authority from applying in your own, and it needs the internet, which your own
controller must not have. So there are two kustomize-controllers, selected by
Flux's sharding label. The second is the FluxInstance shard `connected`
(`spec.sharding`), so it upgrades with the main one:

| | `kustomize-controller` (main) | `kustomize-controller-connected` (shard) |
|---|---|---|
| watches | `sharding.fluxcd.io/key notin (connected)` | `sharding.fluxcd.io/key=connected` |
| `--default-service-account` | **kept** (hosted in-cluster applies) | **none** |
| hub RBAC | tenant impersonation, pinned by `resourceNames`: separate and unchanged | **none beyond running**: Flux CRs, events, leader election, and a per-namespace Secret read |
| internet egress | **none** (declare it that way) | the one controller that has it (guard destinations with `pkg/netguard`) |
| credential to the target | the impersonated tenant ServiceAccount | the uploaded kubeconfig Secret, nothing else |

```kcl
forge.flux_chart(
    connected_shard = True,
    egress = {kustomize = [...], kustomize_connected = [...], source = [...], operator = [...]},
)
# per namespace that holds Flux objects:
forge.flux_namespace_bindings("flux-acme", connected_shard = True)
```

Applying with a ServiceAccount on the shard is **banned, not merely unused**:

- its own ServiceAccount (`kustomize-controller-connected`; the operator's shards
  reuse the main controller's, so a patch swaps it, and a second admission
  policy, `flux-connected-shard-identity`, refuses a shard Deployment with any
  other ServiceAccount or with `--default-service-account`);
- no `--default-service-account` flag, no workload-identity annotation, and a
  ServiceAccount whose roles hold no `impersonate`, no `serviceaccounts` or
  `serviceaccounts/token`, no wildcard and no cluster-wide Secret read (pinned
  over every generated object by `kcl/tests/positive_flux_chart_connected_shard.k`);
  it is never bound `flux-tenant-reconciler`, the role that carries `impersonate`;
- a `ValidatingAdmissionPolicy` (`failurePolicy: Fail`, `Deny`) refuses, on
  CREATE and UPDATE, a Kustomization on the shard that sets
  `spec.serviceAccountName` or `spec.decryption.serviceAccountName`, or lacks
  `spec.kubeConfig.secretRef` (a `configMapRef` resolves a cloud provider
  through the controller's own identity); and, the other way, a Kustomization
  with `spec.kubeConfig` that is not labelled `sharding.fluxcd.io/key: connected`,
  because the main controller's `--default-service-account` would then apply on
  the remote cluster. The policy is run against a real apiserver by
  `TestFluxConnectedShardAdmission`.

The writer of a connected Kustomization sets that label and no
`serviceAccountName` (`flux.ShardLabel` / `flux.ConnectedShard` in Go). The
policy checks writes only: audit existing objects for the label. It needs
`rbac = "namespaced"`, `network_policies = "hardened"` and explicit egress for
`kustomize` and `kustomize_connected` (with open egress on both the split
protects nothing, so the render refuses). The main controller keeps its
hub-side tenant impersonation; that is a separate grant and this does not touch
it.

The shard's Deployment is the operator's, at the same version as the main
controller; it shares placement and priority class. The operator also shards
source-controller; forge deletes that shard from the build, because sources are
never labelled `connected` and the main source-controller watches every one.

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
cluster lacks never schedules. It reaches the Flux pods through the
FluxInstance's pod patches and the operator through `HelmChart.patches`
(`"Deployment/flux-operator" = {...}`), which fails the render, naming the
candidates, if a chart bump renames the Deployment.

**gVisor.** GKE Sandbox's documented gVisor limits (no privileged containers,
no hostPath, no port-forward, seccomp/AppArmor unsupported) are not features the
two controllers use: they run non-root with a read-only root filesystem and
`/tmp` / `/data` emptyDirs, listen on 8080/9090/9440 and call the API server.
Compatibility has not been run under a real sandbox here: verify on the target
(`kubectl -n flux-system get pods -o jsonpath='{.items[*].spec.runtimeClassName}'`,
a reconcile to a new revision, and `kubectl top`) before relying on it, and fall
back to the pool alone if it fails. Patching and CVE watch:
`docs/runbooks/flux-patch-and-upgrade.md`.

> **Operator version ≠ Flux version.** The operator chart (0.62.1) embeds the
> Flux distributions it can install; `FLUX_VERSION` (`2.9.x`) picks the minor.
> Check what a bump would run with the operator's own builder before merging it:
> `flux-operator build instance -f <rendered FluxInstance>`
> (`docs/runbooks/flux-patch-and-upgrade.md`).

### Adopting a Flux installed some other way

The operator adopts an existing install **in place**: it server-side-applies
objects of the same names and takes ownership of fields written by `kubectl`
(forge's apply), `helm` and `kustomize-controller`, so the Deployments keep
their UIDs and selectors and roll like any update; no CRD is deleted and no
Kustomization is pruned. Two rules for moving an env off the community `flux2`
chart forge used to pin:

- **Never re-apply the old chart to remove it.** The new chart has a new name
  (`flux-operator`), so the old chart `flux` is simply no longer declared and
  forge never prunes its inventory. Re-rendering it empty would delete the
  controller Deployments the operator is adopting.
- **Delete the old chart's leftovers by hand afterwards**: the `flux-flux-check`
  Job and ServiceAccount, the `flux-check-egress` NetworkPolicy, and the old
  `flux-edit` / `flux-view` ClusterRoles (the operator renders `-flux-system`
  copies), plus the chart's inventory/lock ConfigMaps in `kube-system`.

### Multitenancy and a bundle carrying cluster-scoped objects

With `rbac = "namespaced"` (or `"pinned"`) every Kustomization that names no
`serviceAccountName` applies as `impersonate_service_account`, which
`flux-tenant-reconciler` grants only per namespace. That is what made #441's
in-cluster Flux fail `Forbidden` on the Namespaces and CRDs a bundle carries.
A self-hosted env should use `applier = "flux"` (above), whose bootstrap derives
those identities from the render. A consumer declaring `forge.flux_chart()` for
its own self-hosted reconciler must either give its Kustomizations (or the
ResourceSet rendering them) identities that may write those kinds, naming each
in `impersonable_service_accounts` so the controller may impersonate it, or run
with `rbac = "upstream"` where no tenant shares the cluster.

### Namespace labels, and what a re-apply prunes

`flux-system` is created by forge (`helm template` never emits a chart's
Namespace), so its metadata is declared on the chart (`flux_chart()` declares
PSA itself, and patches the same labels into the operator's render):

```kcl
forge.flux_chart() | {
    namespace_labels = {
        "pod-security.kubernetes.io/enforce" = "restricted"
        "pod-security.kubernetes.io/enforce-version" = "latest"
    }
}
```

They converge under server-side apply: a changed value is updated, a key you
stop declaring is removed, a key a controller set is left alone, and one a
`kubectl` hand edit set is removed (`deploy/plan`, "The apply removes drift").

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
