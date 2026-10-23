"""
Forge KCL — Flux as a declared platform dependency.

WHY FLUX IS A LIBRARY COMPONENT AND NOT A HIDDEN INSTALL. forge never installs
Flux into a deploy target by itself. An env that runs a Flux of its own (a
control plane reconciling hosted workloads) declares it, because Flux is a
platform dependency — a HelmChart like cert-manager or Envoy Gateway — and a
platform dependency is DECLARED rather than installed by a side effect.

That declaration has to be shared rather than copied, for a reason that bit
control-plane first. cp's `e2e` env ALREADY installs Flux into its
control-plane cluster, for hosted tenants, from its own chart declaration. Two
Flux installs in one cluster collide on the same CRDs and the same
`flux-system` namespace, and the collision is not a clean failure — whichever
applied last owns the CRDs, and the other's Kustomizations reconcile against a
controller that was replaced underneath them.

So forge exports the declaration (`forge.flux_chart()`) instead of owning a
second copy of it. A consumer that needs Flux for its own reasons declares
THIS chart, with its own `values` overlay for whatever identity its registry
needs, and `forge cluster up` then sees a declared chart that already provides
Flux and installs nothing. The detection is by CHART REFERENCE (`FLUX_CHART_OCI`
below), never by the chart's `name`: a name is a `--target` selector the
consumer picks freely, so cp calling its chart "flux" rather than
"flux-system" would otherwise decide whether forge installed a second one.

CHART VERSION vs FLUX VERSION — THEY ARE DIFFERENT NUMBERS, and this is the
mistake to not make on a bump. There is no fluxcd-published helm chart; the
canonical install path is the community chart at
oci://ghcr.io/fluxcd-community/charts/flux2, whose version has its own lineage
that does NOT track the flux2 release. Flux v2.9.5 is the APP version, carried
by chart 2.19.1:

    $ helm show chart oci://ghcr.io/fluxcd-community/charts/flux2 \
          --version 2.19.1 | grep appVersion
    appVersion: 2.9.5

Chart 2.19.0, immediately before it, is appVersion 2.9.1 — so the chart minor
is not a rename of the flux minor and must be RE-DERIVED, never incremented.
Confirm with the command above before editing `FLUX_CHART_VERSION`. The
rendered images are the cross-check: source-controller and kustomize-controller
both v1.9.5.

TWO CONTROLLERS, NOT SIX. Only source-controller (fetches the OCI artifact)
and kustomize-controller (applies it) are installed. helm-controller,
notification-controller, image-reflector-controller and
image-automation-controller are disabled, and that is a security decision
rather than a resource one: every controller installed is attack surface that
has to be patched, and none of these four is on this path. What forge ships in
a bundle is plain Kubernetes objects in an already-rendered artifact — there is
no Helm release to reconcile, no alerting fan-out, and no image-tag automation.
helm-controller specifically carries its own cross-tenant DoS history
(CVE-2022-36049), so installing it "in case" would be paying that exposure for
a feature with no use here.

HARDENED BY DEFAULT, TUNED FOR ONE SHARED CONTROLLER SET. A Flux that reconciles
bundles for more than one author is a shared control point, so `flux_chart()`
ships its hardening and sizing as defaults rather than as a recipe each consumer
re-derives (control-plane first did, as chart overrides, in #899):

  * PER-NAMESPACE RBAC (`rbac = "namespaced"`, the default). The chart's own RBAC lets
    both controllers impersonate ANY ServiceAccount and read EVERY Secret and
    ConfigMap on the cluster. forge replaces it with: the Flux CRs cluster-wide,
    leader election in `flux-system`, and two ClusterRoles
    (`flux-tenant-reconciler`, `flux-tenant-source`) that grant nothing until a
    RoleBinding in a namespace names them (`flux_namespace_bindings()`). Impersonation
    is pinned by `resourceNames` to one ServiceAccount.
  * `DisableConfigWatchers`: without a cluster-wide Secret list the config
    watcher cannot sync, so it is off; a rotated kubeconfig is picked up at the
    next interval.
  * `CancelHealthCheckOnNewRevision`, `--concurrent=16 --concurrent-ssa=8
    --requeue-dependency=10s` (source-controller `--concurrent=8`), requests and
    limits, and a PriorityClass, so one slow rollout cannot hold the queue.
  * NetworkPolicies: `allow-egress` (ingress to every flux-system pod ONLY from
    flux-system pods; an invariant pinned by a test), `allow-scraping` narrowed to
    named namespaces, and no `allow-webhooks`.
  * Pod Security `restricted` on `flux-system` (`namespace_labels`).
  * `placement` (`flux_placement_delivery_v1()`): a dedicated node pool, and a
    sandboxed runtime where the target has one.

Every one is overridable: `values` merges over the posture, the typed arguments
turn a piece off or rename it, and `rbac = "chart"` restores the chart's own RBAC.

THERE IS NO VERSION KNOB, deliberately. The pointer forge writes names an API
version and a `layerSelector` whose behaviour is a property of a specific
source-controller, and the revision-equality check the wait depends on is
likewise. A consumer that could pin a different Flux could pin one where
`lastAppliedRevision` is not the manifest digest, and the wait would then hang
on a comparison that can never be true. `values` remains open — that is the
seam a consumer needs for its own registry identity — but the chart and its
version are forge's.
"""

import regex

# The chart's OCI reference. EXPORTED because it is the SHARED-FLUX
# DETECTOR: `forge cluster up` compares every declared HelmChart's `oci`
# against this string to decide whether a cluster already has a Flux, and
# the Go side (internal/flux.ChartOCI) holds the same spelling. A consumer
# that wants to be recognised as providing Flux declares this ref.
FLUX_CHART_OCI = "oci://ghcr.io/fluxcd-community/charts/flux2"

# Chart 2.19.1 == flux2 v2.9.5 (appVersion). See the header — the two
# numbers are unrelated lineages; re-derive, do not increment.
FLUX_CHART_VERSION = "2.19.1"

# Flux's own namespace. Not configurable: the pointer forge writes goes into
# this namespace by name, and the CRDs the chart installs are cluster-scoped
# either way, so a second spelling would only let the writer and the
# controller disagree about where the OCIRepository lives.
FLUX_NAMESPACE = "flux-system"

# The default `--target` selector. A consumer may name its own chart
# anything; this is the name forge's own install uses.
FLUX_CHART_NAME = "flux"

# The ServiceAccount every Kustomization impersonates when it names none
# (`--default-service-account`), and the ONE name the controller's impersonate
# grant is pinned to. The control plane's per-org namespaces each hold an SA of
# this name; internal/cli/cluster_connect_rbac.go grants the same username on a
# connected cluster. Overridable (`impersonate_service_account`).
FLUX_DEPLOY_SERVICE_ACCOUNT = "reliant-deploy-tenant"

# The two ClusterRoles of the per-namespace RBAC model. They grant NOTHING until
# a RoleBinding in a namespace names them (`flux_namespace_bindings`). The names
# are shared with Go (internal/flux.ReconcilerRole / SourceRole) and
# pinned by a test: a RoleBinding naming a ClusterRole that is not declared is
# accepted by the API server and grants nothing, so drift fails late.
FLUX_RECONCILER_ROLE = "flux-tenant-reconciler"
FLUX_SOURCE_ROLE = "flux-tenant-source"

# The default PriorityClass forge declares for the controllers.
FLUX_PRIORITY_CLASS = "forge-delivery"

# The RuntimeClass GKE Sandbox (and a self-installed gVisor) expose.
FLUX_RUNTIME_CLASS_GVISOR = "gvisor"

_LABEL_RE = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
_SUBDOMAIN_RE = r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
_PLACEMENT_KEYS = ["node_selector", "tolerations", "runtime_class_name"]
_FLUX_LABELS = {"app.kubernetes.io/part-of" = "flux"}
_FLUX_CONTROLLERS = [
    {kind = "ServiceAccount", name = "kustomize-controller", namespace = FLUX_NAMESPACE}
    {kind = "ServiceAccount", name = "source-controller", namespace = FLUX_NAMESPACE}
]
_FLUX_CRS = [
    {apiGroups = ["source.toolkit.fluxcd.io"], resources = ["*"], verbs = ["*"]}
    {apiGroups = ["source.extensions.fluxcd.io"], resources = ["*"], verbs = ["*"]}
    {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["*"], verbs = ["*"]}
]
_FLUX_EVENTS = {apiGroups = [""], resources = ["events"], verbs = ["create", "patch"]}
_FLUX_CONFIG_READ = {apiGroups = [""], resources = ["secrets", "configmaps"], verbs = ["get", "list", "watch"]}

# flux_chart(...) — Flux as a forge.HelmChart, version-pinned, hardened for a
# shared controller set.
#
# `values` is MERGED OVER the posture below, last-wins (maps merge deeply, lists
# replace), which is the seam a consumer needs for its own registry identity.
# `values.extraObjects` is APPENDED to the objects forge generates rather than
# replacing them, so adding one object never costs the RBAC or the policies.
#
#   rbac                       "namespaced" (default): the controllers hold only the
#                              Flux CRs cluster-wide; impersonation and Secret
#                              reads come from per-namespace RoleBindings
#                              (`flux_namespace_bindings`). Turns multitenancy on.
#                              "pinned": the chart's cluster-wide reads, but the
#                              impersonate grant is pinned to one SA by
#                              resourceNames. The step for a consumer that cannot
#                              write per-namespace bindings yet.
#                              "chart": the chart's own RBAC, unchanged.
#   impersonate_service_account     the SA impersonated and pinned (rbac != "chart").
#   network_policies           "hardened" (default) or "chart".
#   scrape_namespaces          namespaces allowed to scrape :8080 (metrics).
#                              Empty (default): nothing outside flux-system.
#   pod_security               PSA level on flux-system ("" to declare none).
#   concurrency / concurrency_ssa / source_concurrency
#                              worker pools (SIZING below).
#   feature_gates              kustomize-controller gates, merged over the
#                              defaults ({"X" = False} turns one off).
#   priority_class_name        "" for none; priority_class_create False to name
#                              a class the cluster already has.
#   placement                  node_selector / tolerations / runtime_class_name,
#                              typically `flux_placement_delivery_v1()`.
#
# Returns a DICT rather than a typed HelmChart because this file is a
# sub-package of the module that declares the schema, and KCL resolves
# package identity such that a sibling lib cannot name a parent-package
# schema. The dict is accepted verbatim into `Bundle.helm_charts:
# [HelmChart]`, which type-checks it at the consumer — so the schema's
# `check:` block still runs, and a malformed chart is refused at the
# declaration rather than at the helm shell-out.
flux_chart = lambda values: {str: any} = {}, \
        cluster: any = Undefined, \
        rbac: "namespaced" | "pinned" | "chart" = "namespaced", \
        impersonate_service_account: str = FLUX_DEPLOY_SERVICE_ACCOUNT, \
        network_policies: "hardened" | "chart" = "hardened", \
        scrape_namespaces: [str] = [], \
        pod_security: str = "restricted", \
        concurrency: int = 16, \
        concurrency_ssa: int = 8, \
        source_concurrency: int = 8, \
        feature_gates: {str: bool} = {}, \
        priority_class_name: str = FLUX_PRIORITY_CLASS, \
        priority_class_create: bool = True, \
        placement: {str: any} = {} -> any {
    assert regex.match(impersonate_service_account, _LABEL_RE), "flux_chart: impersonate_service_account '${impersonate_service_account}' is not an RFC-1123 label"
    assert all ns in scrape_namespaces { regex.match(ns, _LABEL_RE) and len(ns) <= 63 }, "flux_chart: scrape_namespaces entries must be namespace names (RFC-1123 labels); a wildcard is not one, and admitting every namespace is what the chart's own allow-scraping does"
    assert pod_security in ["", "privileged", "baseline", "restricted"], "flux_chart: pod_security must be \"\", \"privileged\", \"baseline\" or \"restricted\", got '${pod_security}'"
    assert concurrency >= 1 and concurrency_ssa >= 1 and source_concurrency >= 1, "flux_chart: concurrency, concurrency_ssa and source_concurrency must be >= 1"
    assert all k in placement { k in _PLACEMENT_KEYS }, "flux_chart: placement keys must be ${_PLACEMENT_KEYS} (build one with flux_placement_delivery_v1())"
    assert not (priority_class_name == "" and priority_class_create), "flux_chart: priority_class_create needs a priority_class_name; pass priority_class_create = False to run without a PriorityClass"
    assert priority_class_name == "" or regex.match(priority_class_name, _SUBDOMAIN_RE), "flux_chart: priority_class_name '${priority_class_name}' is not a valid name"
    assert not ("runtime_class_name" in placement and placement.runtime_class_name != "" and not regex.match(placement.runtime_class_name, _SUBDOMAIN_RE)), "flux_chart: placement.runtime_class_name is not a valid RuntimeClass name"

    _namespaced = rbac == "namespaced"
    _hardened_rbac = rbac != "chart"
    _gates = {
        # A fix-forward revision must not wait out a stuck rollout's timeout.
        CancelHealthCheckOnNewRevision = True
    } | ({
        # Without a cluster-wide Secret list the config watcher cannot sync and
        # the controller never starts.
        DisableConfigWatchers = True
    } if _namespaced else {}) | feature_gates
    _gate_flag = "--feature-gates=" + ",".join(["${k}=${'true' if v else 'false'}" for k, v in _gates])

    _runtime_class = placement?.runtime_class_name or ""
    _tolerations = placement?.tolerations or []
    _node_selector = placement?.node_selector or {}
    _placed = {
        nodeSelector = _node_selector
        tolerations = _tolerations
    } if (_node_selector or _tolerations) else {}

    _generated = (_rbac_objects(rbac, impersonate_service_account) if _hardened_rbac else []) \
        + (_network_policy_objects(scrape_namespaces) if network_policies == "hardened" else []) \
        + (_priority_class_objects(priority_class_name) if priority_class_create else [])

    _posture = {
        # Sized from the head-of-line analysis: the pool is per controller, and
        # a health check holds a worker for its whole timeout.
        kustomizeController = {
            container.additionalArgs = [
                "--no-remote-bases=true"
                "--no-cross-namespace-refs=true"
                "--concurrent=${concurrency}"
                "--concurrent-ssa=${concurrency_ssa}"
                "--requeue-dependency=10s"
                _gate_flag
            ]
            resources = {
                requests = {cpu = "500m", memory = "512Mi"}
                limits = {cpu = "2", memory = "2Gi"}
            }
        } | _placed | ({priorityClassName = priority_class_name} if priority_class_name else {})
        sourceController = {
            container.additionalArgs = ["--concurrent=${source_concurrency}"]
            resources = {
                requests = {cpu = "250m", memory = "256Mi"}
                limits = {cpu = "1", memory = "1Gi"}
            }
        } | _placed | ({priorityClassName = priority_class_name} if priority_class_name else {})
        helmController.create = False
        notificationController.create = False
        imageAutomationController.create = False
        imageReflectionController.create = False
    } | ({
        multitenancy = {
            enabled = True
            defaultServiceAccount = impersonate_service_account
            privileged = False
        }
    } if _hardened_rbac else {}) | ({rbac.create = False} if _hardened_rbac else {}) \
      | ({policies.create = False} if network_policies == "hardened" else {})

    _merged = _posture | values
    _user_extra = values?.extraObjects or []
    _base = {
        name = FLUX_CHART_NAME
        oci = FLUX_CHART_OCI
        version = FLUX_CHART_VERSION
        namespace = FLUX_NAMESPACE
        values = _merged | ({extraObjects = _generated + _user_extra} if (_generated or _user_extra) else {})
    }
    # `cluster` re-targets the install in a multi-cluster env, exactly as it
    # does on any other HelmChart. Added only when given: the schema's field
    # is optional, and a present-but-None `cluster` is not the same as an
    # absent one to a closed schema.
    _base | ({cluster = cluster} if cluster != Undefined else {}) \
          | ({namespace_labels = _psa_labels(pod_security)} if pod_security else {}) \
          | ({patches = _runtime_class_patches(_runtime_class)} if _runtime_class else {})
}

_psa_labels = lambda level: str -> {str: str} {
    {
        "pod-security.kubernetes.io/enforce" = level
        "pod-security.kubernetes.io/enforce-version" = "latest"
        "pod-security.kubernetes.io/warn" = level
        "pod-security.kubernetes.io/audit" = level
    }
}

# The chart has no runtimeClassName value, so it is a patch on the two pods.
_runtime_class_patches = lambda runtime_class: str -> {str: any} {
    {
        "Deployment/kustomize-controller" = {spec.template.spec.runtimeClassName = runtime_class}
        "Deployment/source-controller" = {spec.template.spec.runtimeClassName = runtime_class}
    }
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
flux_namespace_bindings = lambda namespace: str -> [any] {
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
    ]
}

_leader_election_objects = lambda -> [any] {
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
            subjects = _FLUX_CONTROLLERS
        }
    ]
}

# The chart's cluster-wide grants, replaced. `crd-controller` keeps the chart's
# NAME so the live object is narrowed in place rather than left beside a copy.
_rbac_objects = lambda mode: str, target_sa: str -> [any] {
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
    ] + _leader_election_objects() + ([
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "ClusterRole"
            metadata = {name = FLUX_RECONCILER_ROLE, labels = _FLUX_LABELS}
            rules = [
                {apiGroups = [""], resources = ["serviceaccounts"], verbs = ["impersonate"], resourceNames = [target_sa]}
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
            rules = [{apiGroups = [""], resources = ["serviceaccounts"], verbs = ["impersonate"], resourceNames = [target_sa]}]
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

# The chart's three NetworkPolicies minus what a shared controller set must not
# have.
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
_network_policy_objects = lambda scrape_namespaces: [str] -> [any] {
    [
        {
            apiVersion = "networking.k8s.io/v1"
            kind = "NetworkPolicy"
            metadata = {name = "allow-egress", namespace = FLUX_NAMESPACE, labels = _FLUX_LABELS}
            spec = {
                podSelector = {}
                policyTypes = ["Ingress", "Egress"]
                ingress = [{"from" = [{podSelector = {}}]}]
                egress = [{}]
            }
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
