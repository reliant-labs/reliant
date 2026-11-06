"""
Forge KCL — Flux as a declared platform dependency, installed by ControlPlane's
Flux Operator through a FluxInstance.

WHY FLUX IS A LIBRARY COMPONENT AND NOT A HIDDEN INSTALL. forge never installs
Flux into a deploy target by itself. An env that runs a Flux of its own (a
control plane reconciling hosted workloads, or a self-hosted cluster that
pulls its desired state) declares it, because Flux is a platform dependency —
a HelmChart like cert-manager or Envoy Gateway — and a platform dependency is
DECLARED rather than installed by a side effect.

That declaration has to be shared rather than copied, for a reason that bit
control-plane first. Two Flux installs in one cluster collide on the same CRDs
and the same `flux-system` namespace, and the collision is not a clean failure
— whichever applied last owns the CRDs, and the other's Kustomizations
reconcile against a controller that was replaced underneath them. So forge
exports the declaration (`forge.flux_chart()`) and `forge cluster up`
recognises it by CHART REFERENCE (`FLUX_CHART_OCI`), never by the chart's
`name`, which is a `--target` selector the consumer picks freely.

WHAT IS INSTALLED. Two layers, both pinned:

  1. The Flux Operator (https://github.com/controlplaneio-fluxcd/flux-operator,
     by ControlPlane), from its Helm chart at FLUX_OPERATOR_CHART_VERSION with
     the image pinned by DIGEST (FLUX_OPERATOR_IMAGE). It installs the
     FluxInstance, FluxReport, ResourceSet and ResourceSetInputProvider CRDs
     and runs their controllers; none of them is disabled here, so a
     ResourceSet layered on later needs nothing new from this file. The
     operator's web UI is off (it would listen on :9080 with a NetworkPolicy
     admitting every namespace) and so is its aggregation into the cluster's
     admin/edit roles.
  2. A FluxInstance named `flux` (the operator accepts exactly that name, in
     its own namespace), riding the chart as a manifest. The operator renders
     Flux from the distribution manifests EMBEDDED IN ITS OWN IMAGE: with no
     `distribution.artifact`, nothing outside the operator image we pin can
     change what runs. `FLUX_VERSION` pins the MINOR (`2.9.x`); the patch is
     the newest 2.9 the pinned operator embeds, so a Flux patch arrives only
     when FLUX_OPERATOR_CHART_VERSION is bumped. `distribution.artifact` (a
     mirrored `flux-operator-manifests` artifact in your own registry) is the
     seam for shipping Flux CVE patches without an operator bump.

TWO CONTROLLERS, NOT SIX. `components` is source-controller (fetches the OCI
artifact) and kustomize-controller (applies it). helm-controller,
notification-controller, the two image controllers and source-watcher are not
installed, and that is a security decision rather than a resource one: every
controller installed is attack surface that has to be patched, and none of
them is on this path. helm-controller specifically carries its own
cross-tenant DoS history (CVE-2022-36049).

HARDENED BY DEFAULT, TUNED FOR ONE SHARED CONTROLLER SET. The FluxInstance
reaches each controller through `kustomize.patches`, and forge ships the
posture as those patches rather than as a recipe each consumer re-derives:

  * THE CONTROLLER FLAGS ARE REPLACED WHOLESALE, not appended to. The operator
    composes args from profiles (size, multitenancy, sharding), and its
    sharding profile adds `--watch-label-selector=!sharding.fluxcd.io/key` to
    every main controller — which would orphan every object carrying ANY shard
    value (control-plane labels hosted objects `shard0`). So each
    Deployment's `args` is one `replace` patch with exactly the flags below,
    verified against the pinned minor; a minor bump re-derives them
    (docs/runbooks/flux-patch-and-upgrade.md).
  * PER-NAMESPACE RBAC (`rbac = "namespaced"`, the default). The distribution's
    RBAC binds kustomize-controller to cluster-admin
    (`cluster-reconciler-flux-system`) and every controller to a cluster-wide
    Secret/ConfigMap reader (`crd-controller-flux-system`). Both are DELETED
    from the build by patch, and forge declares instead: the Flux CRs
    cluster-wide, leader election in `flux-system`, and two ClusterRoles
    (`flux-tenant-reconciler`, `flux-tenant-source`) that grant nothing until
    a RoleBinding in a namespace names them (`flux_namespace_bindings()`).
    Impersonation is pinned by `resourceNames` to one ServiceAccount. A
    ValidatingAdmissionPolicy (FLUX_RBAC_GUARD_POLICY) refuses any binding to
    a Flux controller ServiceAccount whose role is not one of those, so an
    upstream rename that slipped past a delete patch fails the operator's
    apply loudly instead of granting cluster-admin quietly.
  * `multitenant` with `--default-service-account`, `--no-cross-namespace-refs`
    and `--no-remote-bases`.
  * `DisableConfigWatchers`: without a cluster-wide Secret list the config
    watcher cannot sync, so it is off; a rotated kubeconfig is picked up at the
    next interval.
  * `CancelHealthCheckOnNewRevision`, `--concurrent=16 --concurrent-ssa=8
    --requeue-dependency=10s` (source-controller `--concurrent=8`), requests
    and limits, and a PriorityClass, so one slow rollout cannot hold the queue.
  * NetworkPolicies (`network_policies = "hardened"`): the FluxInstance's own
    `networkPolicy` is OFF and forge declares the policies itself — the stock
    set admits every namespace to :8080 and to notification-controller's
    webhook, and a patch that stopped matching after an upstream change would
    silently bring them back. `allow-egress` (ingress to every flux-system pod
    ONLY from flux-system pods; an invariant pinned by a test),
    `allow-scraping` narrowed to named namespaces, optional per-controller
    egress (the operator included). `network_policies = "upstream"` turns the
    FluxInstance's stock policies on instead.
  * Pod Security `restricted` on `flux-system`, written both on the Namespace
    forge creates and by a patch on the one the operator renders (the operator
    takes ownership of the Namespace and would otherwise strip the label).
  * `enableServiceLinks: false` on every Flux pod and the operator.
  * `placement` (`flux_placement_delivery_v1()`): a dedicated node pool, and a
    sandboxed runtime where the target has one.

DELETION IS NOT UNINSTALLATION. Deleting a FluxInstance makes the operator
delete everything in its inventory — the Flux CRDs included, and with them
every OCIRepository and Kustomization in the cluster. So every CRD the
operator renders is annotated `fluxcd.controlplane.io/prune: disabled` by
patch, and the FluxInstance itself carries `kustomize.toolkit.fluxcd.io/prune:
disabled`, which forge's chart prune honours. Pause the operator with
`fluxcd.controlplane.io/reconcile: disabled` instead of deleting the object.

Every piece is overridable: `values` merges over the operator chart's posture,
`distribution` over the FluxInstance's distribution (registry, variant, pull
secrets, artifact — never `version`), `instance_patches` are appended after
forge's own, and the typed arguments turn a piece off or rename it.

THERE IS NO VERSION KNOB, deliberately. The flags forge writes are verified
against FLUX_VERSION's minor, and a consumer on another minor would run them
against controllers they were never checked on. Bump FLUX_VERSION here.
"""

import regex
import yaml

# The chart forge declares for Flux: the Flux Operator's. EXPORTED because it
# is the SHARED-FLUX DETECTOR: `forge cluster up` compares every declared
# HelmChart's `oci` against this string to decide whether a cluster already
# has a Flux, and the Go side (pkg/flux.ChartOCI) holds the same spelling.
FLUX_CHART_OCI = "oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator"

# The operator chart version; the chart's appVersion is the same number with a
# `v`. Bump together with FLUX_OPERATOR_IMAGE_DIGEST (the multi-arch index
# digest of ghcr.io/controlplaneio-fluxcd/flux-operator:v<version>).
FLUX_CHART_VERSION = "0.62.1"
FLUX_OPERATOR_IMAGE_REPOSITORY = "ghcr.io/controlplaneio-fluxcd/flux-operator"
FLUX_OPERATOR_IMAGE_DIGEST = "sha256:1d09a3f96524d0895fb11e8072d84a3dd029cecdec0f869efa371be6b001469c"
FLUX_OPERATOR_IMAGE = "${FLUX_OPERATOR_IMAGE_REPOSITORY}:v${FLUX_CHART_VERSION}@${FLUX_OPERATOR_IMAGE_DIGEST}"

# The operator's Deployment/ServiceAccount name (the chart's fullnameOverride,
# so a consumer renaming its chart does not rename the pod) and the label its
# pods carry.
FLUX_OPERATOR_NAME = "flux-operator"

# The Flux distribution: a pinned MINOR. The operator resolves the patch from
# the manifests embedded in its pinned image (see the header).
FLUX_VERSION = "2.9.x"
FLUX_REGISTRY = "ghcr.io/fluxcd"

# The FluxInstance's name. The operator reconciles only `flux`.
FLUX_INSTANCE_NAME = "flux"

# Flux's own namespace, and the operator's: a FluxInstance installs Flux into
# the namespace it lives in, which must be the operator's. Not configurable.
FLUX_NAMESPACE = "flux-system"

# The default `--target` selector. A NEW name, not the old community chart's
# "flux", on purpose: forge prunes what a chart stops rendering, and the old
# chart's controller Deployments are exactly what the operator adopts in place.
FLUX_CHART_NAME = "flux-operator"

# The ServiceAccount every Kustomization impersonates when it names none
# (`--default-service-account`), and the ONE name the controller's impersonate
# grant is pinned to. The control plane's per-org namespaces each hold an SA of
# this name; internal/cli/cluster_connect_rbac.go grants the same username on a
# connected cluster. Overridable (`impersonate_service_account`).
FLUX_DEPLOY_SERVICE_ACCOUNT = "reliant-deploy-tenant"

# The ServiceAccount a ResourceSet (or ResourceSetInputProvider) impersonates in
# its own namespace when it names none (the operator's
# `--default-service-account`). Without it a ResourceSet applies as the
# operator, which is cluster-admin; with it, a namespace that grants this SA
# nothing gets nothing applied. "" turns the default off. `flux-delivery` is the
# identity a control plane's per-org ResourceSet is granted in its namespace.
FLUX_RESOURCESET_SERVICE_ACCOUNT = "flux-delivery"

# The two ClusterRoles of the per-namespace RBAC model. They grant NOTHING until
# a RoleBinding in a namespace names them (`flux_namespace_bindings`). The names
# are shared with Go (pkg/flux.ReconcilerRole / SourceRole) and
# pinned by a test: a RoleBinding naming a ClusterRole that is not declared is
# accepted by the API server and grants nothing, so drift fails late.
FLUX_RECONCILER_ROLE = "flux-tenant-reconciler"
FLUX_SOURCE_ROLE = "flux-tenant-source"

# The connected-cluster shard (`connected_shard = True`): a SECOND
# kustomize-controller, the FluxInstance shard `connected`. A Kustomization
# whose `spec.kubeConfig` points at a customer's cluster carries
# `sharding.fluxcd.io/key: connected` and is reconciled ONLY by it; everything
# else stays on the main controller. Shared with Go (pkg/flux) and pinned by a
# test.
FLUX_SHARD_LABEL = "sharding.fluxcd.io/key"
FLUX_CONNECTED_SHARD = "connected"
FLUX_CONNECTED_CONTROLLER = "kustomize-controller-connected"
# The per-namespace Secret read the shard needs (its kubeconfig Secret) and the
# cluster-wide Flux-CR access it needs to run at all. Neither impersonates.
FLUX_CONNECTED_RECONCILER_ROLE = "flux-connected-reconciler"
FLUX_CONNECTED_CONTROLLER_ROLE = "flux-connected-controller"
# The ValidatingAdmissionPolicies (each with its binding) that keep the shard
# service-account-free: one over its Kustomizations, one over its Deployment.
FLUX_CONNECTED_POLICY = "flux-connected-shard-no-serviceaccount"
FLUX_CONNECTED_IDENTITY_POLICY = "flux-connected-shard-identity"
# The ValidatingAdmissionPolicy that refuses an unlisted binding to a Flux
# controller ServiceAccount (rbac != "upstream").
FLUX_RBAC_GUARD_POLICY = "flux-controller-bindings"

# The default PriorityClass forge declares for the controllers.
FLUX_PRIORITY_CLASS = "forge-delivery"

# The RuntimeClass GKE Sandbox (and a self-installed gVisor) expose.
FLUX_RUNTIME_CLASS_GVISOR = "gvisor"

_LABEL_RE = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
_SUBDOMAIN_RE = r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
_PLACEMENT_KEYS = ["node_selector", "tolerations", "runtime_class_name"]
_DISTRIBUTION_KEYS = ["registry", "variant", "imagePullSecret", "artifact", "artifactPullSecret"]
_FLUX_LABELS = {"app.kubernetes.io/part-of" = "flux"}
_FLUX_CONTROLLERS = [
    {kind = "ServiceAccount", name = "kustomize-controller", namespace = FLUX_NAMESPACE}
    {kind = "ServiceAccount", name = "source-controller", namespace = FLUX_NAMESPACE}
]
_FLUX_CONNECTED_SA = {kind = "ServiceAccount", name = FLUX_CONNECTED_CONTROLLER, namespace = FLUX_NAMESPACE}

# The pods `egress` can name, and what selects each one's pods.
_EGRESS_KEYS = ["kustomize", "source", "kustomize_connected", "operator"]
_EGRESS_POLICY_NAMES = {
    kustomize = "kustomize-controller-egress"
    source = "source-controller-egress"
    kustomize_connected = "kustomize-controller-connected-egress"
    operator = "flux-operator-egress"
}
_EGRESS_SELECTORS = {
    kustomize = {app = "kustomize-controller"}
    source = {app = "source-controller"}
    kustomize_connected = {app = FLUX_CONNECTED_CONTROLLER}
    operator = {"app.kubernetes.io/name" = FLUX_OPERATOR_NAME}
}
# Egress left unspecified for a controller: open, as Flux ships it.
_EGRESS_OPEN = [{}]
_FLUX_CRS = [
    {apiGroups = ["source.toolkit.fluxcd.io"], resources = ["*"], verbs = ["*"]}
    {apiGroups = ["source.extensions.fluxcd.io"], resources = ["*"], verbs = ["*"]}
    {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["*"], verbs = ["*"]}
]
_FLUX_EVENTS = {apiGroups = [""], resources = ["events"], verbs = ["create", "patch"]}
_FLUX_CONFIG_READ = {apiGroups = [""], resources = ["secrets", "configmaps"], verbs = ["get", "list", "watch"]}

# The distribution's RBAC, rendered with the operator's `-<namespace>` suffix.
# rbac != "upstream" deletes the two that grant authority; flux-edit/flux-view
# (aggregation into admin/edit/view) and the critical-pods ResourceQuota stay.
_UPSTREAM_GRANTS = "(cluster-reconciler|crd-controller)-${FLUX_NAMESPACE}"

# Every role a Flux controller ServiceAccount may be bound to, as
# "<roleRef.kind>/<roleRef.name>" (FLUX_RBAC_GUARD_POLICY).
_ALLOWED_CONTROLLER_ROLES = [
    "ClusterRole/crd-controller"
    "Role/flux-leader-election"
    "ClusterRole/${FLUX_RECONCILER_ROLE}"
    "ClusterRole/${FLUX_SOURCE_ROLE}"
    "ClusterRole/${FLUX_CONNECTED_CONTROLLER_ROLE}"
    "ClusterRole/${FLUX_CONNECTED_RECONCILER_ROLE}"
    "ClusterRole/cluster-reconciler-impersonator"
]

# flux_chart(...) — the Flux Operator as a forge.HelmChart, with the hardened
# FluxInstance riding it. Hub and self-hosted alike: nothing here knows about a
# control plane.
#
#   values                     MERGED OVER the operator chart's posture below,
#                              last-wins (maps merge deeply, lists replace).
#   cluster                    re-targets the install in a multi-cluster env.
#   rbac                       "namespaced" (default): the controllers hold only the
#                              Flux CRs cluster-wide; impersonation and Secret
#                              reads come from per-namespace RoleBindings
#                              (`flux_namespace_bindings`). Turns multitenancy on.
#                              "pinned": cluster-wide Secret/ConfigMap reads, but
#                              the impersonate grant is pinned to one SA by
#                              resourceNames. The step for a consumer that cannot
#                              write per-namespace bindings yet.
#                              "upstream": the distribution's own RBAC
#                              (kustomize-controller is cluster-admin) and no
#                              multitenancy lockdown.
#   impersonate_service_account     the SA impersonated and pinned (rbac != "upstream").
#   impersonable_service_accounts   further SA names the impersonate grant is pinned
#                              to, beside impersonate_service_account (which stays
#                              the default for a Kustomization that names none).
#                              For a cluster whose Kustomizations name their own
#                              apply identities (a self-hosted delivery's derived
#                              ones, lib/flux_delivery.k). Still granted only in
#                              namespaces that carry flux_namespace_bindings.
#   resourceset_service_account     the SA a ResourceSet impersonates by default
#                              ("" for none; FLUX_RESOURCESET_SERVICE_ACCOUNT).
#   override_managers          field managers whose ownership the operator takes
#                              over when it applies an object (`--override-manager`,
#                              repeatable): the writer a ResourceSet or the
#                              FluxInstance adopts objects from. kubectl, helm and
#                              kustomize-controller are taken over already.
#   network_policies           "hardened" (default) or "upstream".
#   scrape_namespaces          namespaces allowed to scrape :8080 (metrics).
#                              Empty (default): nothing outside flux-system.
#   pod_security               PSA level on flux-system ("" to declare none).
#   concurrency / concurrency_ssa / source_concurrency
#                              worker pools (sized from the head-of-line
#                              analysis: a health check holds a worker for its
#                              whole timeout).
#   feature_gates              kustomize-controller gates, merged over the
#                              defaults ({"X" = False} turns one off).
#   priority_class_name        "" for the distribution's own; priority_class_create
#                              False to name a class the cluster already has.
#   placement                  node_selector / tolerations / runtime_class_name,
#                              typically `flux_placement_delivery_v1()`; applied
#                              to the controllers AND the operator.
#   egress                     per-pod NetworkPolicy egress rules, keyed
#                              "kustomize" | "source" | "kustomize_connected" |
#                              "operator". Default (empty): every pod keeps open
#                              egress. Once ANY key is given every flux-system
#                              pod is default-deny and each must be named (an
#                              explicit [{}] opts one back into open egress). The
#                              operator needs the Kubernetes API, plus the
#                              registry of `distribution.artifact` when set.
#   connected_shard            True adds the FluxInstance shard `connected`: a
#                              SECOND kustomize-controller for connected
#                              clusters (below). Needs rbac = "namespaced" and
#                              explicit egress for "kustomize" and
#                              "kustomize_connected".
#   distribution               merged over {registry = FLUX_REGISTRY}: `registry`
#                              + `variant` for a mirror, `imagePullSecret`,
#                              `artifact` / `artifactPullSecret` for a mirrored
#                              manifests artifact. `version` is refused.
#   instance_patches           FluxInstance `kustomize.patches` entries, appended
#                              AFTER forge's (so they win where they overlap).
#   extra_objects              objects appended to the riding manifests.
#
# THE CONNECTED SHARD. A Kustomization with `spec.kubeConfig` (apply to a
# customer's cluster) must never be applied by a controller that can also act
# as a hub identity, and the controller that dials customer clusters is the one
# that needs the internet. So they are two controllers:
#
#   kustomize-controller (main)      keeps --default-service-account and the
#                                    tenant impersonation on the hub; watches
#                                    `sharding.fluxcd.io/key notin (connected)`
#                                    (so `shard0` and unlabelled objects stay
#                                    on it); give it NO internet egress.
#   kustomize-controller-connected   watches `sharding.fluxcd.io/key=connected`;
#                                    runs as its OWN ServiceAccount (the
#                                    operator's shards reuse the main one, so a
#                                    patch swaps it), with NO
#                                    --default-service-account, no workload
#                                    identity, no impersonate and no
#                                    serviceaccounts access on the hub; its only
#                                    credential to a connected cluster is the
#                                    kubeconfig Secret the Kustomization names.
#                                    It is the one controller given internet
#                                    egress.
#
# The operator also shards source-controller; that shard is deleted by patch.
# Sources are never labelled `connected`, and the main source-controller keeps
# no label selector at all, so every source stays on it.
#
# "Applies with a service account" is BANNED on the shard, not merely unused:
# a ValidatingAdmissionPolicy refuses a connected-shard Kustomization that sets
# spec.serviceAccountName (or spec.decryption.serviceAccountName) or lacks
# spec.kubeConfig.secretRef, and a Kustomization with spec.kubeConfig that is
# not on the shard; a second refuses a shard Deployment that runs as any other
# ServiceAccount or passes --default-service-account.

# Returns a DICT rather than a typed HelmChart because this file is a
# sub-package of the module that declares the schema, and KCL resolves
# package identity such that a sibling lib cannot name a parent-package
# schema. The dict is accepted verbatim into `Bundle.helm_charts:
# [HelmChart]`, which type-checks it at the consumer — so the schema's
# `check:` block still runs.
flux_chart = lambda values: {str: any} = {}, \
        cluster: any = Undefined, \
        rbac: "namespaced" | "pinned" | "upstream" = "namespaced", \
        impersonate_service_account: str = FLUX_DEPLOY_SERVICE_ACCOUNT, \
        impersonable_service_accounts: [str] = [], \
        resourceset_service_account: str = FLUX_RESOURCESET_SERVICE_ACCOUNT, \
        override_managers: [str] = [], \
        network_policies: "hardened" | "upstream" = "hardened", \
        scrape_namespaces: [str] = [], \
        pod_security: str = "restricted", \
        concurrency: int = 16, \
        concurrency_ssa: int = 8, \
        source_concurrency: int = 8, \
        feature_gates: {str: bool} = {}, \
        priority_class_name: str = FLUX_PRIORITY_CLASS, \
        priority_class_create: bool = True, \
        placement: {str: any} = {}, \
        egress: {str: [{str: any}]} = {}, \
        connected_shard: bool = False, \
        distribution: {str: str} = {}, \
        instance_patches: [{str: any}] = [], \
        extra_objects: [any] = [] -> any {
    assert regex.match(impersonate_service_account, _LABEL_RE), "flux_chart: impersonate_service_account '${impersonate_service_account}' is not an RFC-1123 label"
    assert all sa in impersonable_service_accounts { regex.match(sa, _LABEL_RE) }, "flux_chart: impersonable_service_accounts ${impersonable_service_accounts} must all be RFC-1123 labels (ServiceAccount names); a wildcard would let the controller impersonate any ServiceAccount in a bound namespace"
    assert resourceset_service_account == "" or regex.match(resourceset_service_account, _LABEL_RE), "flux_chart: resourceset_service_account '${resourceset_service_account}' is not an RFC-1123 label"
    assert all m in override_managers { m and " " not in m and "=" not in m }, "flux_chart: override_managers entries must be field-manager names"
    assert all ns in scrape_namespaces { regex.match(ns, _LABEL_RE) and len(ns) <= 63 }, "flux_chart: scrape_namespaces entries must be namespace names (RFC-1123 labels); a wildcard is not one, and admitting every namespace is what Flux's own allow-scraping does"
    assert pod_security in ["", "privileged", "baseline", "restricted"], "flux_chart: pod_security must be \"\", \"privileged\", \"baseline\" or \"restricted\", got '${pod_security}'"
    assert concurrency >= 1 and concurrency_ssa >= 1 and source_concurrency >= 1, "flux_chart: concurrency, concurrency_ssa and source_concurrency must be >= 1"
    assert all k in placement { k in _PLACEMENT_KEYS }, "flux_chart: placement keys must be ${_PLACEMENT_KEYS} (build one with flux_placement_delivery_v1())"
    assert not (priority_class_name == "" and priority_class_create), "flux_chart: priority_class_create needs a priority_class_name; pass priority_class_create = False to run without a PriorityClass of forge's"
    assert priority_class_name == "" or regex.match(priority_class_name, _SUBDOMAIN_RE), "flux_chart: priority_class_name '${priority_class_name}' is not a valid name"
    assert not ("runtime_class_name" in placement and placement.runtime_class_name != "" and not regex.match(placement.runtime_class_name, _SUBDOMAIN_RE)), "flux_chart: placement.runtime_class_name is not a valid RuntimeClass name"
    assert all k in distribution { k in _DISTRIBUTION_KEYS }, "flux_chart: distribution keys must be ${_DISTRIBUTION_KEYS}; the Flux version is forge's (FLUX_VERSION), not a consumer's"

    assert all k in egress { k in _EGRESS_KEYS }, "flux_chart: egress keys must be ${_EGRESS_KEYS}, got ${[k for k in egress]}"
    assert connected_shard or "kustomize_connected" not in egress, "flux_chart: egress['kustomize_connected'] names a controller that only exists with connected_shard = True"
    assert not connected_shard or rbac == "namespaced", "flux_chart: connected_shard needs rbac = \"namespaced\": the shard holds no cluster-wide Secret read, so it only works where Secret reads come from per-namespace RoleBindings (flux_namespace_bindings(ns, connected_shard = True))"
    assert not connected_shard or network_policies == "hardened", "flux_chart: connected_shard needs network_policies = \"hardened\": Flux's own policies leave every controller's egress open, which is the thing the shard exists to split"
    assert not connected_shard or ("kustomize" in egress and "kustomize_connected" in egress), "flux_chart: connected_shard needs explicit egress for \"kustomize\" and \"kustomize_connected\": with the default (open) egress on both, the controller that holds hub impersonation can still reach the internet and the split protects nothing. Pass egress = {kustomize = [...], kustomize_connected = [...]} (an explicit [{}] opts back in)"

    _hardened_rbac = rbac != "upstream"
    _gates = {
        # A fix-forward revision must not wait out a stuck rollout's timeout.
        CancelHealthCheckOnNewRevision = True
    } | ({
        # Without a cluster-wide Secret list the config watcher cannot sync and
        # the controller never starts.
        DisableConfigWatchers = True
    } if rbac == "namespaced" else {}) | feature_gates
    _gate_flag = "--feature-gates=" + ",".join(["${k}=${'true' if v else 'false'}" for k, v in _gates])

    _runtime_class = placement?.runtime_class_name or ""
    _tolerations = placement?.tolerations or []
    _node_selector = placement?.node_selector or {}

    _main_selector = "${FLUX_SHARD_LABEL} notin (${FLUX_CONNECTED_SHARD})" if connected_shard else ""
    _instance = _flux_instance({
        hardened_rbac = _hardened_rbac
        impersonate = impersonate_service_account
        upstream_policies = network_policies == "upstream"
        connected = connected_shard
        distribution = {registry = FLUX_REGISTRY} | distribution
        patches = _instance_patches({
            hardened_rbac = _hardened_rbac
            connected = connected_shard
            pod_security = pod_security
            kustomize_args = _kustomize_args(impersonate_service_account if _hardened_rbac else "", concurrency, concurrency_ssa, _gate_flag, _main_selector)
            source_args = _source_args(source_concurrency)
            connected_args = _connected_args(concurrency, concurrency_ssa, _gate_flag)
            priority_class_name = priority_class_name
            node_selector = _node_selector
            tolerations = _tolerations
            runtime_class = _runtime_class
        }) + instance_patches
    })
    _impersonable = [impersonate_service_account] + [sa for sa in impersonable_service_accounts if sa != impersonate_service_account]
    _riding = (_rbac_objects(rbac, _impersonable, connected_shard) if _hardened_rbac else []) \
        + (_network_policy_objects(scrape_namespaces, egress, connected_shard) if network_policies == "hardened" else []) \
        + (_priority_class_objects(priority_class_name) if priority_class_create else []) \
        + (_connected_shard_objects() if connected_shard else []) \
        + [_instance] + extra_objects

    _posture = {
        fullnameOverride = FLUX_OPERATOR_NAME
        image = {repository = FLUX_OPERATOR_IMAGE_REPOSITORY, tag = "v${FLUX_CHART_VERSION}@${FLUX_OPERATOR_IMAGE_DIGEST}"}
        # The web UI listens on :9080 behind a NetworkPolicy admitting every
        # namespace; nothing here uses it.
        web.enabled = False
        # Aggregating the Flux Operator's kinds into admin/edit hands every
        # namespace admin the ability to write a ResourceSet.
        rbac.createAggregation = False
        multitenancy = {
            enabled = resourceset_service_account != ""
            defaultServiceAccount = resourceset_service_account or FLUX_OPERATOR_NAME
        }
        installCRDs = True
    } | ({extraArgs = ["--override-manager=${m}" for m in override_managers]} if override_managers else {}) \
      | ({priorityClassName = priority_class_name} if priority_class_name else {}) \
      | ({nodeSelector = _node_selector} if _node_selector else {}) \
      | ({tolerations = _tolerations} if _tolerations else {})

    _base = {
        name = FLUX_CHART_NAME
        oci = FLUX_CHART_OCI
        version = FLUX_CHART_VERSION
        namespace = FLUX_NAMESPACE
        values = _posture | values
        manifests = _riding
        patches = {
            "Deployment/${FLUX_OPERATOR_NAME}" = {spec.template.spec = {enableServiceLinks = False} | ({runtimeClassName = _runtime_class} if _runtime_class else {})}
        }
    }
    # `cluster` re-targets the install in a multi-cluster env, exactly as it
    # does on any other HelmChart. Added only when given: the schema's field
    # is optional, and a present-but-None `cluster` is not the same as an
    # absent one to a closed schema.
    _base | ({cluster = cluster} if cluster != Undefined else {}) \
          | ({namespace_labels = _psa_labels(pod_security)} if pod_security else {})
}

_psa_labels = lambda level: str -> {str: str} {
    {
        "pod-security.kubernetes.io/enforce" = level
        "pod-security.kubernetes.io/enforce-version" = "latest"
        "pod-security.kubernetes.io/warn" = level
        "pod-security.kubernetes.io/audit" = level
    }
}

# ── The FluxInstance ───────────────────────────────────────────────

_flux_instance = lambda o: {str: any} -> any {
    {
        apiVersion = "fluxcd.controlplane.io/v1"
        kind = "FluxInstance"
        metadata = {
            name = FLUX_INSTANCE_NAME
            namespace = FLUX_NAMESPACE
            labels = _FLUX_LABELS
            annotations = {
                "fluxcd.controlplane.io/reconcileEvery" = "1h"
                "fluxcd.controlplane.io/reconcileTimeout" = "5m"
                # Deleting the FluxInstance UNINSTALLS Flux; forge's chart prune
                # must never be what does it (see the header).
                "kustomize.toolkit.fluxcd.io/prune" = "disabled"
            }
        }
        spec = {
            distribution = {version = FLUX_VERSION} | o.distribution
            components = ["source-controller", "kustomize-controller"]
            cluster = {
                type = "kubernetes"
                multitenant = o.hardened_rbac
                networkPolicy = o.upstream_policies
                domain = "cluster.local"
            } | ({tenantDefaultServiceAccount = o.impersonate} if o.hardened_rbac else {})
            kustomize.patches = o.patches
        } | ({sharding = {key = FLUX_SHARD_LABEL, shards = [FLUX_CONNECTED_SHARD]}} if o.connected else {})
    }
}

# The flags, complete. Each list REPLACES the Deployment's args (see the
# header): what is not here is not passed.
_kustomize_args = lambda impersonate: str, concurrency: int, concurrency_ssa: int, gate_flag: str, selector: str -> [str] {
    [
        "--watch-all-namespaces=true"
        "--log-level=info"
        "--log-encoding=json"
        "--enable-leader-election"
        "--no-remote-bases=true"
        "--no-cross-namespace-refs=true"
    ] + (["--default-service-account=${impersonate}"] if impersonate else []) + [
        "--concurrent=${concurrency}"
        "--concurrent-ssa=${concurrency_ssa}"
        "--requeue-dependency=10s"
        gate_flag
    ] + (["--watch-label-selector=${selector}"] if selector else [])
}

_source_args = lambda concurrency: int -> [str] {
    [
        "--watch-all-namespaces=true"
        "--log-level=info"
        "--log-encoding=json"
        "--enable-leader-election"
        "--storage-path=/data"
        "--storage-adv-addr=source-controller.$(RUNTIME_NAMESPACE).svc.cluster.local."
        "--concurrent=${concurrency}"
    ]
}

_KC_RESOURCES = {requests = {cpu = "500m", memory = "512Mi"}, limits = {cpu = "2", memory = "2Gi"}}
_SC_RESOURCES = {requests = {cpu = "250m", memory = "256Mi"}, limits = {cpu = "1", memory = "1Gi"}}

_target = lambda kind: str, name: str -> {str: str} { {kind = kind, name = "^${name}$"} }
_json_patch = lambda kind: str, name: str, ops: [any] -> any { {target = _target(kind, name), patch = yaml.encode(ops)} }
_merge_patch = lambda api_version: str, kind: str, name: str, body: {str: any} -> any {
    {target = _target(kind, name), patch = yaml.encode({apiVersion = api_version, kind = kind, metadata.name = name} | body)}
}
_delete_patch = lambda api_version: str, kind: str, name: str -> any {
    {target = {kind = kind, name = name}, patch = yaml.encode({"$patch" = "delete", apiVersion = api_version, kind = kind, metadata.name = "all"})}
}

# The pod-level posture one controller Deployment gets.
_pod_patch = lambda name: str, resources: {str: any}, o: {str: any} -> any {
    _merge_patch("apps/v1", "Deployment", name, {spec.template.spec = {
        enableServiceLinks = False
        containers = [{name = "manager", resources = resources}]
        # "" keeps the distribution's own class (system-cluster-critical).
    } | ({priorityClassName = o.priority_class_name} if o.priority_class_name else {}) \
      | ({nodeSelector = o.node_selector} if o.node_selector else {}) \
      | ({tolerations = o.tolerations} if o.tolerations else {}) \
      | ({runtimeClassName = o.runtime_class} if o.runtime_class else {})})
}

_instance_patches = lambda o: {str: any} -> [any] {
    # Every CRD survives a FluxInstance deletion or a component removal.
    [{target.kind = "CustomResourceDefinition", patch = yaml.encode({apiVersion = "apiextensions.k8s.io/v1", kind = "CustomResourceDefinition", metadata = {name = "all", annotations = {"fluxcd.controlplane.io/prune" = "disabled"}}})}] \
    + ([_merge_patch("v1", "Namespace", FLUX_NAMESPACE, {metadata.labels = _psa_labels(o.pod_security)})] if o.pod_security else []) \
    + [
        _json_patch("Deployment", "kustomize-controller", [{op = "replace", path = "/spec/template/spec/containers/0/args", value = o.kustomize_args}])
        _json_patch("Deployment", "source-controller", [{op = "replace", path = "/spec/template/spec/containers/0/args", value = o.source_args}])
        _pod_patch("kustomize-controller", _KC_RESOURCES, o)
        _pod_patch("source-controller", _SC_RESOURCES, o)
    ] + ([
        _json_patch("Deployment", FLUX_CONNECTED_CONTROLLER, [
            {op = "replace", path = "/spec/template/spec/containers/0/args", value = o.connected_args}
            {op = "replace", path = "/spec/template/spec/serviceAccountName", value = FLUX_CONNECTED_CONTROLLER}
        ])
        _pod_patch(FLUX_CONNECTED_CONTROLLER, _KC_RESOURCES, o)
        _delete_patch("apps/v1", "Deployment", "^source-controller-${FLUX_CONNECTED_SHARD}$")
        _delete_patch("v1", "Service", "^source-controller-${FLUX_CONNECTED_SHARD}$")
    ] if o.connected else []) + ([
        _delete_patch("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "^${_UPSTREAM_GRANTS}$")
        _delete_patch("rbac.authorization.k8s.io/v1", "ClusterRole", "^${_UPSTREAM_GRANTS}$")
    ] if o.hardened_rbac else [])
}

# flux_placement_delivery_v1() — the `placement` for a dedicated delivery pool.
#
# forge has no node-pool primitive, so the pool itself is provisioned outside
# KCL (tainted `<pool_key>=<pool_value>:NoSchedule`, labelled the same). This
# only makes the controllers land there and nowhere else, away from
# internet-facing pods: a node escape from one of those must not reach the
# controllers' tokens.
#
# `runtime_class_name` adds a sandbox. Pass FLUX_RUNTIME_CLASS_GVISOR on a
# target that has it (GKE Sandbox on the pool); "" (default) is the pool alone.
# forge cannot know the target, so a sandbox is never defaulted on: a pod naming
# a RuntimeClass the cluster lacks never schedules.
flux_placement_delivery_v1 = lambda runtime_class_name: str = "", \
        pool_key: str = "reliant.dev/pool", \
        pool_value: str = "delivery-v1" -> {str: any} {
    {
        node_selector = {"${pool_key}" = pool_value}
        tolerations = [{key = pool_key, operator = "Equal", value = pool_value, effect = "NoSchedule"}]
        runtime_class_name = runtime_class_name
    }
}

# flux_namespace_bindings(namespace) — the two RoleBindings that give the shared
# controllers authority in ONE namespace (rbac = "namespaced").
#
#   kustomize-controller → flux-tenant-reconciler: impersonate
#       `<namespace>:<impersonate_service_account>`, read the namespace's
#       Secrets/ConfigMaps (a connected cluster's kubeconfig).
#   source-controller → flux-tenant-source: read the namespace's
#       Secrets/ConfigMaps (the registry pull secret).
#
# Whoever creates a namespace that holds Flux objects applies these with it
# (the control plane does, from Go; a static namespace declares them in KCL).
# The destination namespace needs no controller grant: the apply, the health
# check and the prune all run as the impersonated identity.
#
# `connected_shard = True` adds the third: the connected shard's ServiceAccount
# → flux-connected-reconciler, which reads Secrets in the namespace (the
# kubeconfig Secret) and nothing else. It is a SEPARATE binding because the
# shard must never receive flux-tenant-reconciler, which carries impersonate.
flux_namespace_bindings = lambda namespace: str, connected_shard: bool = False -> [any] {
    assert regex.match(namespace, _LABEL_RE) and len(namespace) <= 63, "flux_namespace_bindings: '${namespace}' is not a namespace name"
    [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "RoleBinding"
            metadata = {name = role, namespace = namespace, labels = _FLUX_LABELS}
            subjects = [subject]
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = role}
        } for role, subject in {
            "${FLUX_RECONCILER_ROLE}" = _FLUX_CONTROLLERS[0]
            "${FLUX_SOURCE_ROLE}" = _FLUX_CONTROLLERS[1]
        }
    ] + ([{
        apiVersion = "rbac.authorization.k8s.io/v1"
        kind = "RoleBinding"
        metadata = {name = FLUX_CONNECTED_RECONCILER_ROLE, namespace = namespace, labels = _FLUX_LABELS}
        subjects = [_FLUX_CONNECTED_SA]
        roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = FLUX_CONNECTED_RECONCILER_ROLE}
    }] if connected_shard else [])
}

_leader_election_objects = lambda connected: bool = False -> [any] {
    [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "Role"
            metadata = {name = "flux-leader-election", namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            rules = [
                {apiGroups = [""], resources = ["configmaps"], verbs = ["get", "list", "watch", "create", "update", "patch", "delete"]}
                {apiGroups = [""], resources = ["configmaps/status"], verbs = ["get", "update", "patch"]}
                {apiGroups = ["coordination.k8s.io"], resources = ["leases"], verbs = ["get", "list", "watch", "create", "update", "patch", "delete"]}
            ]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "RoleBinding"
            metadata = {name = "flux-leader-election", namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "Role", name = "flux-leader-election"}
            subjects = _FLUX_CONTROLLERS + ([_FLUX_CONNECTED_SA] if connected else [])
        }
    ]
}

# The distribution's cluster-wide grants, replaced (`_UPSTREAM_GRANTS` deletes
# the operator's copies). `crd-controller` keeps the name the community chart
# used, so an install that came from that chart is narrowed in place rather
# than left beside a copy.
_rbac_objects = lambda mode: str, target_sas: [str], connected: bool = False -> [any] {
    [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = "crd-controller", labels = _FLUX_LABELS}
            # Tenant mode reads the ServiceAccount (the impersonator's existence
            # check is a cluster-wide informer) but no Secret and no ConfigMap.
            rules = _FLUX_CRS + [
                {apiGroups = [""], resources = ["namespaces", "serviceaccounts"], verbs = ["get", "list", "watch"]}
                _FLUX_EVENTS
            ] + ([_FLUX_CONFIG_READ] if mode == "pinned" else [])
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRoleBinding"
            metadata = {name = "crd-controller", labels = _FLUX_LABELS}
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = "crd-controller"}
            subjects = _FLUX_CONTROLLERS
        }
    ] + _leader_election_objects(connected) + (_connected_rbac_objects() if connected else []) + _rbac_guard_objects() + ([
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = FLUX_RECONCILER_ROLE, labels = _FLUX_LABELS}
            rules = [
                {apiGroups = [""], resources = ["serviceaccounts"], verbs = ["impersonate"], resourceNames = target_sas}
                _FLUX_CONFIG_READ
            ]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = FLUX_SOURCE_ROLE, labels = _FLUX_LABELS}
            rules = [_FLUX_CONFIG_READ]
        }
    ] if mode == "namespaced" else [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = "cluster-reconciler-impersonator", labels = _FLUX_LABELS}
            rules = [{apiGroups = [""], resources = ["serviceaccounts"], verbs = ["impersonate"], resourceNames = target_sas}]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRoleBinding"
            metadata = {name = "cluster-reconciler-impersonator", labels = _FLUX_LABELS}
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = "cluster-reconciler-impersonator"}
            subjects = [_FLUX_CONTROLLERS[0]]
        }
    ])
}

# Flux's three stock NetworkPolicies (the FluxInstance's `networkPolicy`)
# minus what a shared controller set must not have. Declared by forge rather
# than patched into the operator's, so a patch that stops matching can never
# silently restore the stock ones.
#
#   * allow-egress — the INVARIANT. Ingress to every flux-system pod ONLY from
#     flux-system pods; egress unrestricted. It keeps source-controller's
#     artifact server (every author's fetched bundle) and the controllers'
#     endpoints unreachable from any other namespace. Pinned by tests.
#   * allow-scraping — :8080 from the namespaces the consumer names, not from
#     every namespace; absent when none is named.
#   * allow-webhooks — dropped. It admits every namespace to
#     notification-controller, which this posture does not install, so it was
#     inert; it is the policy GHSA-mwcp-qpcg-fr7c is about.
_network_policy_objects = lambda scrape_namespaces: [str], egress: {str: [{str: any}]} = {}, connected: bool = False -> [any] {
    # No `egress` declared: Flux's own open egress, byte-for-byte what
    # forge has always rendered. Declared: every flux-system pod is selected for
    # Egress with NO rule (default deny) and each controller's policy below adds
    # back exactly what it was given. Fail-closed rather than "unnamed stays
    # open", so a controller or Job added later cannot silently have the
    # internet.
    _restricted = len(egress) > 0
    _ctrls = ["kustomize", "source"] + (["kustomize_connected"] if connected else []) + ["operator"]
    assert not _restricted or all c in _ctrls { c in egress }, "flux_chart: egress is declared for ${[k for k in egress]} but not for ${[c for c in _ctrls if c not in egress]}; once egress is declared every flux-system pod is default-deny, so name each pod (an explicit [{}] opts one back into open egress)"
    _per_controller = [
        {
            apiVersion = "networking.k8s.io/v1"
            kind = "NetworkPolicy"
            metadata = {name = _EGRESS_POLICY_NAMES[c], namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            spec = {
                podSelector.matchLabels = _EGRESS_SELECTORS[c]
                policyTypes = ["Egress"]
                egress = egress[c]
            }
        } for c in _ctrls
    ] if _restricted else []
    # Per-controller allow-lists are listed BEFORE allow-egress so that, when
    # this is applied over a live open policy, the allows exist before the
    # deny takes effect.
    _per_controller + [
        {
            apiVersion = "networking.k8s.io/v1"
            kind = "NetworkPolicy"
            metadata = {name = "allow-egress", namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            spec = {
                podSelector = {}
                policyTypes = ["Ingress", "Egress"]
                ingress = [{"from" = [{podSelector = {}}]}]
            } | ({} if _restricted else {egress = _EGRESS_OPEN})
        }
    ] + ([
        {
            apiVersion = "networking.k8s.io/v1"
            kind = "NetworkPolicy"
            metadata = {name = "allow-scraping", namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            spec = {
                podSelector = {}
                policyTypes = ["Ingress"]
                ingress = [{
                    "from" = [{namespaceSelector.matchExpressions = [{key = "kubernetes.io/metadata.name", operator = "In", values = scrape_namespaces}]}]
                    ports = [{port = 8080, protocol = "TCP"}]
                }]
            }
        }
    ] if scrape_namespaces else [])
}

# ── The connected-cluster shard ────────────────────────────────────

# The shard's own flags. Deliberately NOT here: --default-service-account. With
# it, Flux impersonates a ServiceAccount even on the remote cluster named by
# spec.kubeConfig, which would turn the uploaded token into "a token that can
# impersonate a name in someone else's cluster". Without it, a Kustomization
# applies as the kubeconfig's own identity or not at all.
_connected_args = lambda concurrency: int, concurrency_ssa: int, gate_flag: str -> [str] {
    [
        "--watch-all-namespaces=true"
        "--log-level=info"
        "--log-encoding=json"
        "--enable-leader-election"
        "--no-remote-bases=true"
        "--no-cross-namespace-refs=true"
        "--concurrent=${concurrency}"
        "--concurrent-ssa=${concurrency_ssa}"
        "--requeue-dependency=10s"
        gate_flag
        "--watch-label-selector=${FLUX_SHARD_LABEL}=${FLUX_CONNECTED_SHARD}"
    ]
}

# What the shard may do on the HUB. Every rule is something the controller needs
# to run at all; nothing lets it act as anyone:
#   * no `impersonate`, no `serviceaccounts` (get/list/watch or /token), no
#     wildcard verb or resource, no cluster-wide Secret or ConfigMap read;
#   * its Secret read is per-namespace (flux_namespace_bindings(ns,
#     connected_shard = True)), so a namespace with no binding is unreachable;
#   * its only credential to a connected cluster is the kubeconfig Secret a
#     Kustomization names.
# `test` cases in kcl/tests pin this over every generated object, so a rule
# added here later that reintroduces authority fails the build.
_connected_rbac_objects = lambda -> [any] {
    [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = FLUX_CONNECTED_CONTROLLER_ROLE, labels = _FLUX_LABELS}
            rules = [
                {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["kustomizations"], verbs = ["get", "list", "watch", "create", "update", "patch", "delete"]}
                {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["kustomizations/status"], verbs = ["get", "update", "patch"]}
                {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["kustomizations/finalizers"], verbs = ["get", "create", "update", "patch", "delete"]}
                {apiGroups = ["source.toolkit.fluxcd.io"], resources = ["buckets", "gitrepositories", "ocirepositories"], verbs = ["get", "list", "watch"]}
                {apiGroups = ["source.toolkit.fluxcd.io"], resources = ["buckets/status", "gitrepositories/status", "ocirepositories/status"], verbs = ["get"]}
                {apiGroups = ["source.extensions.fluxcd.io"], resources = ["externalartifacts"], verbs = ["get", "list", "watch"]}
                _FLUX_EVENTS
            ]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRoleBinding"
            metadata = {name = FLUX_CONNECTED_CONTROLLER_ROLE, labels = _FLUX_LABELS}
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = FLUX_CONNECTED_CONTROLLER_ROLE}
            subjects = [_FLUX_CONNECTED_SA]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = FLUX_CONNECTED_RECONCILER_ROLE, labels = _FLUX_LABELS}
            rules = [{apiGroups = [""], resources = ["secrets"], verbs = ["get", "list", "watch"]}]
        }
    ]
}

# The shard's ServiceAccount and admission policies. Its Deployment is the
# operator's (the FluxInstance shard), so it upgrades with the main controller;
# `_instance_patches` gives it its flags and this ServiceAccount.
_connected_shard_objects = lambda -> [any] {
    [
        {
            apiVersion = "v1"
            kind = "ServiceAccount"
            metadata = {name = FLUX_CONNECTED_CONTROLLER, namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS | {"app.kubernetes.io/component" = FLUX_CONNECTED_CONTROLLER}}
        }
    ] + _connected_admission_objects() + _connected_identity_objects()
}

# THE IDENTITY, held by admission. The shard Deployment is rendered by the
# operator and given its ServiceAccount and flags by patch; a patch that
# stopped matching after an upstream change would leave the shard running as
# kustomize-controller (which holds the tenant impersonation) with internet
# egress. This refuses that Deployment instead, so the operator's apply fails
# and the FluxInstance reports it, rather than the split quietly ending.
_connected_identity_objects = lambda -> [any] {
    [
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicy"
            metadata = {name = FLUX_CONNECTED_IDENTITY_POLICY, labels = _FLUX_LABELS}
            spec = {
                failurePolicy = "Fail"
                matchConstraints.resourceRules = [{
                    apiGroups = ["apps"]
                    apiVersions = ["*"]
                    operations = ["CREATE", "UPDATE"]
                    resources = ["deployments"]
                }]
                matchConditions = [{
                    name = "the-connected-shard"
                    expression = "request.namespace == '${FLUX_NAMESPACE}' && object.metadata.name == '${FLUX_CONNECTED_CONTROLLER}'"
                }]
                validations = [
                    {
                        expression = "has(object.spec.template.spec.serviceAccountName) && object.spec.template.spec.serviceAccountName == '${FLUX_CONNECTED_CONTROLLER}'"
                        message = "the connected-cluster controller must run as the ${FLUX_CONNECTED_CONTROLLER} ServiceAccount, which can impersonate nothing"
                        reason = "Invalid"
                    }
                    {
                        expression = "object.spec.template.spec.containers.all(c, !has(c.args) || c.args.all(a, !a.startsWith('--default-service-account')))"
                        message = "the connected-cluster controller must not pass --default-service-account: it would impersonate a name in the customer's cluster"
                        reason = "Invalid"
                    }
                ]
            }
        }
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicyBinding"
            metadata = {name = FLUX_CONNECTED_IDENTITY_POLICY, labels = _FLUX_LABELS}
            spec = {
                policyName = FLUX_CONNECTED_IDENTITY_POLICY
                validationActions = ["Deny"]
            }
        }
    ]
}

# THE BINDING GUARD (rbac != "upstream"). The distribution's own grants are
# deleted from the operator's build by patch (`_UPSTREAM_GRANTS`). If an upstream
# rename made that patch stop matching, kustomize-controller would be bound to
# cluster-admin and nothing would say so. This refuses any RoleBinding or
# ClusterRoleBinding naming a Flux controller ServiceAccount (or a group that
# contains one) unless its role is one forge declares, so the operator's apply
# fails loudly instead. Only writes are checked: a binding that predates the
# policy is the audit's to find.
_rbac_guard_objects = lambda -> [any] {
    _sas = "['kustomize-controller', 'source-controller', '${FLUX_CONNECTED_CONTROLLER}']"
    # A CEL list literal: KCL's own list interpolation leaves strings unquoted.
    _allowed = "[" + ", ".join(["'${r}'" for r in _ALLOWED_CONTROLLER_ROLES]) + "]"
    _subject = "(s.kind == 'ServiceAccount' && s.name in ${_sas} && (has(s.namespace) ? s.namespace : request.namespace) == '${FLUX_NAMESPACE}') || (s.kind == 'Group' && s.name in ['system:serviceaccounts', 'system:serviceaccounts:${FLUX_NAMESPACE}'])"
    [
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicy"
            metadata = {name = FLUX_RBAC_GUARD_POLICY, labels = _FLUX_LABELS}
            spec = {
                failurePolicy = "Fail"
                matchConstraints.resourceRules = [{
                    apiGroups = ["rbac.authorization.k8s.io"]
                    apiVersions = ["*"]
                    operations = ["CREATE", "UPDATE"]
                    resources = ["rolebindings", "clusterrolebindings"]
                }]
                variables = [{name = "namesFlux", expression = "has(object.subjects) && object.subjects.exists(s, ${_subject})"}]
                validations = [{
                    expression = "!variables.namesFlux || (object.roleRef.kind + '/' + object.roleRef.name) in ${_allowed}"
                    messageExpression = "'a Flux controller ServiceAccount may be bound only to ${', '.join(_ALLOWED_CONTROLLER_ROLES)}; refusing ' + object.roleRef.kind + '/' + object.roleRef.name"
                    reason = "Forbidden"
                }]
            }
        }
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicyBinding"
            metadata = {name = FLUX_RBAC_GUARD_POLICY, labels = _FLUX_LABELS}
            spec = {
                policyName = FLUX_RBAC_GUARD_POLICY
                validationActions = ["Deny"]
            }
        }
    ]
}

# THE BAN. "Apply with a service account" is refused on the shard by admission,
# not left to convention, so a Kustomization routed to it cannot be made to
# impersonate (through spec.serviceAccountName, or through
# spec.decryption.serviceAccountName, which mints a token for KMS) and cannot
# lean on cloud identity (spec.kubeConfig.configMapRef resolves a provider
# through the controller's own identity). Both directions are checked, so the
# shard label cannot be dropped to put a kubeConfig Kustomization on the main
# controller, whose --default-service-account would then apply on the remote
# cluster.
#
# failurePolicy Fail: if the policy cannot be evaluated the write is refused.
# Pre-existing objects are not re-checked (UPDATE only fires on a write), so
# the label is the gate to look for in an audit, not this policy alone.
_connected_admission_objects = lambda -> [any] {
    _shard = "has(object.metadata.labels) && '${FLUX_SHARD_LABEL}' in object.metadata.labels && object.metadata.labels['${FLUX_SHARD_LABEL}'] == '${FLUX_CONNECTED_SHARD}'"
    [
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicy"
            metadata = {name = FLUX_CONNECTED_POLICY, labels = _FLUX_LABELS}
            spec = {
                failurePolicy = "Fail"
                matchConstraints.resourceRules = [{
                    apiGroups = ["kustomize.toolkit.fluxcd.io"]
                    apiVersions = ["*"]
                    operations = ["CREATE", "UPDATE"]
                    resources = ["kustomizations"]
                }]
                variables = [
                    {name = "onShard", expression = _shard}
                    {name = "hasKubeConfig", expression = "has(object.spec.kubeConfig)"}
                ]
                validations = [
                    {
                        expression = "!variables.onShard || !has(object.spec.serviceAccountName) || object.spec.serviceAccountName == ''"
                        message = "a Kustomization on the connected shard must not set spec.serviceAccountName: the connected-cluster controller never applies as a service account"
                        reason = "Invalid"
                    }
                    {
                        expression = "!variables.onShard || !has(object.spec.decryption) || !has(object.spec.decryption.serviceAccountName) || object.spec.decryption.serviceAccountName == ''"
                        message = "a Kustomization on the connected shard must not set spec.decryption.serviceAccountName"
                        reason = "Invalid"
                    }
                    {
                        expression = "!variables.onShard || (variables.hasKubeConfig && has(object.spec.kubeConfig.secretRef))"
                        message = "a Kustomization on the connected shard must set spec.kubeConfig.secretRef: the uploaded kubeconfig Secret is its only credential"
                        reason = "Invalid"
                    }
                    {
                        expression = "!variables.hasKubeConfig || variables.onShard"
                        message = "a Kustomization with spec.kubeConfig must carry the label ${FLUX_SHARD_LABEL}=${FLUX_CONNECTED_SHARD}: only the connected-cluster controller may dial a connected cluster"
                        reason = "Invalid"
                    }
                ]
            }
        }
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicyBinding"
            metadata = {name = FLUX_CONNECTED_POLICY, labels = _FLUX_LABELS}
            spec = {
                policyName = FLUX_CONNECTED_POLICY
                validationActions = ["Deny"]
            }
        }
    ]
}

# A PriorityClass below the system classes: a rollout's surge pod elsewhere must
# not preempt the controller, and the controller must not be starved on a busy
# node. Declared here because a pod naming a class the cluster lacks is rejected
# at admission, so a default NAME with no object would be a broken default.
_priority_class_objects = lambda name: str -> [any] {
    [{
        apiVersion = "scheduling.k8s.io/v1"
        kind = "PriorityClass"
        metadata = {name = name, labels = _FLUX_LABELS}
        value = 1000000
        globalDefault = False
        description = "Flux source-controller and kustomize-controller: the delivery path other workloads are rolled out through."
    }]
}
