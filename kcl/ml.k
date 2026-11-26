"""
Forge KCL — the ML stack of a cluster you run
(docs/proposals/ml-workloads.md §3.1).

    forge.MLStack                 what one cluster runs for ML: one per cluster
    forge.Adopt                   a component that is already there: forge reads it
    forge.Install                 a component forge installs, from a pinned chart
    forge.Accelerators            one accelerator vendor on the cluster
    forge.RecommendationDecision  a dismissed recommendation, reasoned and expiring

The policy the types encode: discover, then adopt, then install only what is
missing. `forge ml discover` reads the cluster with your kube context
(read-only) and proposes this declaration; you review it and commit it.
Every component slot is `forge.Adopt` (a selector over what already runs,
never touched), `forge.Install` (a pinned chart forge delivers, in the env's
bundle or as a bootstrap chart, and labels as its own), or absent. A kind is never both, never installed
twice, and never installed when discovery found one running: two DCGM
exporters on one node break each other's profiling counters.

forge ships no per-vendor preset here. Every vendor fact (the DCGM metric
allow list, an exporter's port and labels) is the author's data, which
`forge ml discover` writes into the env's own file, beside the declaration
it describes.

An env that declares no stack renders exactly as before.

Splitting the stack into its own file: discover writes it to
`deploy/kcl/<env>/ml.k`, beside main.k, and main.k wires it:

    # deploy/kcl/prod/ml.k
    import forge
    ml_stacks = [forge.MLStack {...}]

    # deploy/kcl/prod/main.k
    import forge
    import .ml
    output = forge.render(forge.Bundle {
        ...
        ml_stacks = ml.ml_stacks
    })

It renders whether forge is handed main.k or the env directory
(internal/kclrender sibling_import_eval_test.go). Given the directory, every
.k in it is one package, so `ml_stacks` also appears beside `output` in the
raw document; forge reads only `output`.
"""

import regex

# A component kind: a known component id ("dcgm-exporter", "kueue",
# "node-problem-detector") or one of your own, as an RFC-1123 label.
_ML_KIND_RE: str = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
# A recommendation rule id (pkg/mlrecs), e.g. "kubelet-tracing-off".
_ML_RULE_ID_RE: str = r"^[a-z0-9]+(-[a-z0-9]+)*$"
# A Prometheus exposition name: what an Adopt `allow` entry names.
_ML_METRIC_RE: str = r"^[a-zA-Z_:][a-zA-Z0-9_:]*$"

# Kinds forge never installs unless the author states why: the Install check
# refuses them without a `reason`. Ray (KubeRay) is adopt-only by policy: it
# is a whole platform with its own scheduler, and forge installs a team's
# queue and training defaults (Kueue + JobSet, Kubeflow Trainer) instead.
_ML_ADOPT_ONLY: [str] = ["kuberay"]

# The extended resource each vendor's device plugin advertises, the default
# of Accelerators.resource. "generic" has none: it names its own.
ML_ACCELERATOR_RESOURCES: {str:str} = {
    "nvidia": "nvidia.com/gpu"
    "amd": "amd.com/gpu"
    "tpu": "google.com/tpu"
    "neuron": "aws.amazon.com/neuroncore"
    "gaudi": "habana.ai/gaudi"
}

# The service.name prefix of a component's scrape.
ML_SERVICE_PREFIX: str = "ml-"
# The label value forge stamps as forge.dev/component on every object an
# Install renders: ML_COMPONENT_PREFIX + kind.
ML_COMPONENT_PREFIX: str = "ml-"

type MLComponent = Adopt | Install

schema Adopt:
    """A component already running on the cluster, which forge reads and
    never touches: no upgrade, no reconfiguration, no ownership.

        # found by forge ml discover, 2026-11-25: dcgm-exporter 3.3.9 via GPU Operator v24.9
        metrics = forge.Adopt {
            kind = "dcgm-exporter", namespace = "gpu-operator", version = "3.3.9"
            selector = {"app": "nvidia-dcgm-exporter"}, port = 9400
            allow = ["DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_XID_ERRORS"]
        }
        queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}

    With a `selector` and a `port` it lowers to a forge.Telemetry scrape in
    the env's observability (the Observability.telemetry semantics:
    service_name `ml-<kind>`, suffixed with the cluster when the env has
    several stacks, scope "platform", the stack's cluster, `namespaces =
    [namespace]`, one MetricsScrape on `port` keeping only `allow`). That
    happens only when the env's observability is on. A Telemetry the env
    declares itself with the same service_name replaces it: that is how a
    scrape gets a path, an interval or dashboards of your own.

    Without a port it lowers to nothing in the cluster: it is an inventory
    assertion, which tools compare against what the cluster runs.

    `allow` is the author's data, never a forge preset: the names you draw
    and alert on, which `forge ml discover` writes from the exporter version
    it found. A scrape without it keeps every name.
    """
    # The component, as a known id ("dcgm-exporter", "gpu-operator",
    # "kueue", "kserve", "kube-state-metrics") or one of your own.
    kind: str
    # Where it runs (unset: any namespace the selector matches). Defaulted
    # to None, not left Undefined: an unset attribute would resolve to the
    # package's `namespace` helper (base.k) inside the check block.
    namespace?: str = None
    # Pod labels that must ALL match: what to scrape.
    selector?: {str:str}
    # The metrics port on the matched pods (needs `selector`).
    port?: int
    # The exposition names the scrape keeps (needs `port`).
    allow: [str] = []
    # What discovery found running. Informational.
    version?: str
    # A note for reviewers.
    note?: str
    # The scrape's service.name, instead of ml-<kind>[-<cluster>]: for two
    # scraped adoptions of one kind (two Prometheus installs).
    service_name?: str

    check:
        regex.match(kind, _ML_KIND_RE), \
            "Adopt.kind '${kind}' must be an RFC-1123 label (a component id such as dcgm-exporter)"
        not namespace or regex.match(namespace, _ML_KIND_RE), \
            "Adopt '${kind}'.namespace '${namespace}' must be an RFC-1123 label"
        not selector or all k, v in selector { k and v }, \
            "Adopt '${kind}'.selector keys and values must be non-empty"
        not port or selector, \
            "Adopt '${kind}' sets port = ${port} without a selector: the port is scraped on the pods a selector matches. Add selector = {\"<label>\": \"<value>\"}, or drop port to declare the component without scraping it"
        typeof(port) != "int" or 0 < port <= 65535, \
            "Adopt '${kind}'.port must be 1-65535"
        not allow or port, \
            "Adopt '${kind}' sets allow without a port: allow filters a scrape. Add selector and port, or drop allow"
        all m in allow { regex.match(m, _ML_METRIC_RE) }, \
            "Adopt '${kind}'.allow names ${[m for m in allow if not regex.match(m, _ML_METRIC_RE)]}, which are not Prometheus metric names"
        not service_name or port, \
            "Adopt '${kind}' sets service_name without a port: only a scraped adoption has a service.name"
        not service_name or regex.match(service_name, _TEL_NAME_RE), \
            "Adopt '${kind}'.service_name '${service_name}' must match ${_TEL_NAME_RE}"

schema Install:
    """A component forge installs because the cluster lacks it, from a pinned
    upstream chart.

        node_health = forge.Install {
            kind = "node-problem-detector"
            chart = forge.HelmChart {
                name = "node-problem-detector", repo = "https://charts.deliveryhero.io/"
                chart = "node-problem-detector", version = "2.3.14"
                namespace = "forge-ml", delivery = "bundle"
            }
        }
        queue = forge.Install {
            kind = "kueue"
            chart = forge.HelmChart {
                name = "kueue", oci = "oci://registry.k8s.io/kueue/charts/kueue"
                version = "0.14.4", namespace = "kueue-system"   # delivery "bootstrap" (the default)
            }
        }

    It lowers to that HelmChart on the stack's cluster (`cluster` defaults to
    the stack's), delivered the way the chart's `delivery` says:

      * "bundle", for a chart that owns no CRDs (node-problem-detector,
        dcgm-exporter, Spegel): rendered with `helm template --skip-crds` at
        build time into the env's bundle and reconciled by the env's
        delivery (the hub's Flux), which prunes it when the Install leaves the
        file. forge creates no Namespace for a bundle chart, so the lowering
        renders the chart's Namespace itself (labelled with the component
        owner, never pruned: deleting a namespace deletes everything in it),
        unless the env already renders or declares it.
      * "bootstrap", for an operator with CRDs (Kueue, JobSet, Kubeflow
        Trainer): applied with `forge env deploy <env> --target <chart name>`
        and kept out of the bundle, so a bundle prune can never delete its
        CRDs and every object made from them. forge creates the chart's
        Namespace, and applies the CRDs the chart TEMPLATES first, waiting
        until they are Established. A chart that ships its CRDs in its
        `crds/` directory instead gets none applied (`--skip-crds`): declare
        them in a forge.Manifests group, or pick a chart that templates them.

    On both paths every object the chart renders is labelled
    `forge.dev/component-owner = <project>.<env>` and
    `forge.dev/component = ml-<kind>`, so discovery tells forge's installs
    from the cluster's own.

    `selector`, `port` and `allow` scrape what it installed, exactly as on a
    forge.Adopt (`namespaces = [chart.namespace]`): an exporter forge
    installs is scraped like one it adopts.

    Kinds in the adopt-only set (KubeRay) are refused unless `reason` says
    why this cluster needs forge to install one.
    """
    kind: str
    chart: HelmChart
    # Why forge installs it: required for an adopt-only kind (kuberay).
    reason?: str
    # Pod labels that must ALL match: what to scrape (as on forge.Adopt).
    selector?: {str:str}
    # The metrics port on the matched pods (needs `selector`).
    port?: int
    # The exposition names the scrape keeps (needs `port`).
    allow: [str] = []
    # The scrape's service.name, instead of ml-<kind>[-<cluster>].
    service_name?: str

    check:
        regex.match(kind, _ML_KIND_RE), \
            "Install.kind '${kind}' must be an RFC-1123 label (a component id such as node-problem-detector)"
        chart.delivery != "bundle" or (not chart.patches and not chart.manifests), \
            "Install '${kind}': chart '${chart.name}' sets patches or manifests, which a delivery 'bundle' chart does not apply. Use the chart's values, Bundle.overrides for a field its values do not reach, or delivery = \"bootstrap\""
        kind not in _ML_ADOPT_ONLY or (reason and reason.strip()), \
            "Install of kind '${kind}' is refused: ${kind} (Ray) is adopt-only by policy, so forge never installs it by default. Adopt the one the cluster runs with forge.Adopt {kind = \"${kind}\"}, or state why forge must install it: reason = \"<why this cluster needs it>\""
        not selector or all k, v in selector { k and v }, \
            "Install '${kind}'.selector keys and values must be non-empty"
        not port or selector, \
            "Install '${kind}' sets port = ${port} without a selector: the port is scraped on the pods a selector matches. Add selector = {\"<label>\": \"<value>\"}, or drop port"
        typeof(port) != "int" or 0 < port <= 65535, \
            "Install '${kind}'.port must be 1-65535"
        not allow or port, \
            "Install '${kind}' sets allow without a port: allow filters a scrape. Add selector and port, or drop allow"
        all m in allow { regex.match(m, _ML_METRIC_RE) }, \
            "Install '${kind}'.allow names ${[m for m in allow if not regex.match(m, _ML_METRIC_RE)]}, which are not Prometheus metric names"
        not service_name or port, \
            "Install '${kind}' sets service_name without a port: only a scraped component has a service.name"
        not service_name or regex.match(service_name, _TEL_NAME_RE), \
            "Install '${kind}'.service_name '${service_name}' must match ${_TEL_NAME_RE}"

schema Accelerators:
    """One accelerator vendor on the cluster.

        accelerators = [forge.Accelerators {
            vendor = "nvidia"
            metrics = forge.Adopt {kind = "dcgm-exporter", namespace = "gpu-operator", selector = {"app": "nvidia-dcgm-exporter"}, port = 9400}
        }]

    `resource` is the extended resource the vendor's device plugin
    advertises: nvidia.com/gpu, amd.com/gpu, google.com/tpu,
    aws.amazon.com/neuroncore or habana.ai/gaudi by default; a "generic"
    vendor names its own. `metrics` is the vendor's exporter, adopted or
    installed. `node_selector` narrows the nodes that carry it.
    """
    vendor: "nvidia" | "amd" | "tpu" | "neuron" | "gaudi" | "generic"
    resource?: str = ML_ACCELERATOR_RESOURCES[vendor] if vendor in ML_ACCELERATOR_RESOURCES else None
    metrics?: MLComponent
    node_selector?: {str:str}

    check:
        resource, \
            "Accelerators vendor = \"${vendor}\" needs resource: the extended resource its device plugin advertises (e.g. \"example.com/npu\")"
        not node_selector or all k, v in node_selector { k and v }, \
            "Accelerators '${vendor}'.node_selector keys and values must be non-empty"

schema RecommendationDecision:
    """A recommendation you dismissed, with the claim a reviewer can check and
    an expiry: past `expires` the recommendation shows again.

        forge.RecommendationDecision {
            id = "kubelet-tracing-off", decision = "dismiss"
            reason = "GKE does not expose kubelet tracing config", expires = "2027-06-01"
        }

    It lowers to nothing in the cluster; the recommendation rules read it
    (pkg/mlrecs). Accepting a recommendation needs no record: the accepted
    fix is the declaration.
    """
    # The rule's id.
    id: str
    decision: "dismiss" = "dismiss"
    reason: str
    # YYYY-MM-DD: a dismissal is time-boxed, never open-ended.
    expires: str

    check:
        regex.match(id, _ML_RULE_ID_RE), \
            "RecommendationDecision.id '${id}' must be a rule id (lowercase words joined by '-', e.g. kubelet-tracing-off)"
        reason.strip() != "", \
            "RecommendationDecision '${id}' needs a reason: the claim a reviewer can check"
        regex.match(expires, _DELIVERY_DATE_RE), \
            "RecommendationDecision '${id}'.expires '${expires}' must be a YYYY-MM-DD date: a dismissal is time-boxed, it cannot be open-ended"

schema MLStack:
    """What one cluster runs for ML, each component adopted or installed.
    Set on `Bundle.ml_stacks`, one per cluster.

        ml_stacks = [forge.MLStack {
            cluster = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"}
            # Component kinds the last `forge ml discover` found running that forge did not install.
            observed = ["gpu-operator", "dcgm-exporter", "kueue"]
            observed_at = "2026-11-25"
            accelerators = [forge.Accelerators {
                vendor = "nvidia"
                metrics = forge.Adopt {
                    kind = "dcgm-exporter", namespace = "gpu-operator"
                    selector = {"app": "nvidia-dcgm-exporter"}, port = 9400
                    allow = _DCGM_ALLOW   # written by forge ml discover
                }
            }]
            node_health = forge.Install {
                kind = "node-problem-detector"
                chart = forge.HelmChart {name = "node-problem-detector", repo = "https://charts.deliveryhero.io/",
                                         chart = "node-problem-detector", version = "2.3.14", namespace = "forge-ml", delivery = "bundle"}
            }
            queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}
            training = [forge.Adopt {kind = "jobset"}]
            serving = [forge.Adopt {kind = "kserve"}]
            monitoring = [forge.Adopt {kind = "kube-state-metrics", namespace = "monitoring"}]
            decisions = [forge.RecommendationDecision {
                id = "kubelet-tracing-off", decision = "dismiss"
                reason = "GKE does not expose kubelet tracing config", expires = "2027-06-01"
            }]
        }]

    The refusals, each naming the kind and the fix: a kind both adopted and
    installed; a kind installed twice; an Install of a kind in `observed`
    (adopt the running one instead); two Accelerators of one vendor; a
    decision id twice; an Install chart bound to another cluster.

    What renders (`forge env render <env> --json`):

      * each Install: a `bundled_charts` entry (delivery "bundle") or a
        `helm_charts` entry (delivery "bootstrap") on the stack's cluster,
        with the `labels` forge stamps on every object the chart renders;
      * each bundle Install's namespace: a Namespace in `manifests`, unless
        the env already renders or declares it;
      * each scraped component (an Adopt or an Install with selector and
        port): a `telemetry` target (attachment "observability", source
        `ml-stack:<cluster>`), when observability is on;
      * `output.ml_stacks`, the declared stack for tools to read without
        parsing KCL (forge ml discover, the control plane), one entry per
        stack:

            {
              "cluster": "<kube context>", "namespace": "<ClusterTarget.namespace>",
              "observed": ["<kind>", ...], "observed_at": "<YYYY-MM-DD or empty>",
              "accelerators": [{"vendor", "resource", "node_selector"}],
              "components": [{
                "slot": "accelerators" | "node_health" | "queue" | "training" |
                        "serving" | "monitoring" | "pull_cache",
                "vendor": "<the Accelerators vendor, slot accelerators only, else empty>",
                "mode": "adopt" | "install", "kind", "namespace",
                "version": "<Adopt.version, or the Install chart's version>",
                "selector", "port" (0 when unset), "allow" (the scrape, either mode),
                "note",
                "service_name": "<the scrape's service.name when it renders, else empty>",
                "chart": "<the Install chart's name, else empty>", "reason"
              }],
              "decisions": [{"id", "decision", "reason", "expires"}]
            }

        Every key is always present (empty when unset); keys are only ever
        added.
    """
    # Where the stack lives. `cluster` is the kube context.
    cluster: ClusterTarget
    accelerators: [Accelerators] = []
    node_health?: MLComponent
    queue?: MLComponent
    training: [MLComponent] = []
    serving: [MLComponent] = []
    monitoring: [MLComponent] = []
    # An image pull cache (Spegel, a registry mirror).
    pull_cache?: MLComponent
    # Component kinds the last `forge ml discover` found running that forge
    # did not install. An Install of one is refused.
    observed: [str] = []
    # When that discovery ran, YYYY-MM-DD.
    observed_at?: str
    decisions: [RecommendationDecision] = []

    check:
        all k in observed { regex.match(k, _ML_KIND_RE) }, \
            "MLStack '${cluster.cluster}'.observed names ${[k for k in observed if not regex.match(k, _ML_KIND_RE)]}, which are not component kinds (RFC-1123 labels)"
        not observed_at or regex.match(observed_at, _DELIVERY_DATE_RE), \
            "MLStack '${cluster.cluster}'.observed_at '${observed_at}' must be a YYYY-MM-DD date"
        len(_ml_both(_ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache))) == 0, \
            "MLStack '${cluster.cluster}' both adopts and installs ${_ml_both(_ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache))}: a component is adopted (read where it runs) or installed (forge's own), never both. Keep the forge.Adopt if the cluster already runs it; otherwise drop the Adopt"
        len(_duplicates([e.c.kind for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == "Install"])) == 0, \
            "MLStack '${cluster.cluster}' installs ${_duplicates([e.c.kind for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == 'Install'])} twice: one install of a component per cluster. Remove the duplicate forge.Install"
        len([e.c.kind for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == "Install" and e.c.kind in observed]) == 0, \
            "MLStack '${cluster.cluster}': discovery found ${[e.c.kind for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == 'Install' and e.c.kind in observed]} running (observed); adopt it with forge.Adopt instead of installing a second one — two DCGM exporters on one node break each other's profiling counters. If it is gone, re-run forge ml discover so observed says so"
        len(_duplicates([a.vendor for a in accelerators])) == 0, \
            "MLStack '${cluster.cluster}' declares accelerators of vendor ${_duplicates([a.vendor for a in accelerators])} twice: one forge.Accelerators per vendor. Merge them"
        len(_duplicates([d.id for d in decisions])) == 0, \
            "MLStack '${cluster.cluster}' decides recommendation ${_duplicates([d.id for d in decisions])} twice: one forge.RecommendationDecision per rule id. Keep one"
        len([e.c.chart.name for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == "Install" and e.c.chart.cluster and _eso_context(e.c.chart) != cluster.cluster]) == 0, \
            "MLStack '${cluster.cluster}' installs chart ${[e.c.chart.name for e in _ml_slot_entries(accelerators, node_health, queue, training, serving, monitoring, pull_cache) if typeof(e.c) == 'Install' and e.c.chart.cluster and _eso_context(e.c.chart) != cluster.cluster]} on another cluster: an Install lands on its stack's cluster. Drop the chart's cluster (it defaults to the stack's), or move the Install to that cluster's stack"

# ---------------------------------------------------------------------------
# Lowering helpers (wired in kcl/render.k and the Bundle check).
# ---------------------------------------------------------------------------

# Every component of a stack, in slot order: {slot, vendor, c}. Fields, not
# the MLStack, so the MLStack check can call it.
_ml_slot_entries = lambda accelerators: [any], node_health: any, queue: any, training: [any], serving: [any], monitoring: [any], pull_cache: any -> [any] {
    [{"slot": "accelerators", "vendor": a.vendor, "c": a.metrics} for a in accelerators if a.metrics] \
        + ([{"slot": "node_health", "vendor": "", "c": node_health}] if node_health else []) \
        + ([{"slot": "queue", "vendor": "", "c": queue}] if queue else []) \
        + [{"slot": "training", "vendor": "", "c": c} for c in training] \
        + [{"slot": "serving", "vendor": "", "c": c} for c in serving] \
        + [{"slot": "monitoring", "vendor": "", "c": c} for c in monitoring] \
        + ([{"slot": "pull_cache", "vendor": "", "c": pull_cache}] if pull_cache else [])
}

_ml_entries = lambda s: MLStack -> [any] {
    _ml_slot_entries(s.accelerators, s.node_health, s.queue, s.training, s.serving, s.monitoring, s.pull_cache)
}

# The kinds a stack both adopts and installs, sorted.
_ml_both = lambda entries: [any] -> [str] {
    _adopted = [e.c.kind for e in entries if typeof(e.c) == "Adopt"]
    sorted({e.c.kind: 0 for e in entries if typeof(e.c) == "Install" and e.c.kind in _adopted})
}

# A kube context as a service.name suffix (the CloudNativePG operator
# target's spelling).
_ml_ctx_suffix = lambda ctx: str -> str {
    regex.replace(ctx.lower(), r"[^-a-z0-9_.]", "-").strip("-_.")
}

# The service.name of a component's scrape, or "" when it declares no port:
# its own service_name, else ml-<kind>, suffixed with the cluster when the env
# has several stacks. Adopt and Install scrape alike.
_ml_scrape_svc = lambda c: any, ctx: str, nstacks: int -> str {
    (c.service_name or (ML_SERVICE_PREFIX + c.kind + ("-" + _ml_ctx_suffix(ctx) if nstacks > 1 else ""))) if c.port else ""
}

# The namespace a component's scrape discovers pods in: the adoption's, or
# the installed chart's ("" for any namespace).
_ml_scrape_ns = lambda c: any -> str {
    (c.namespace or "") if typeof(c) == "Adopt" else c.chart.namespace
}

# Every scraped component of the env's stacks: {svc, cluster, c}.
_ml_scrapes = lambda stacks: [any] -> [any] {
    [{
        "svc": _ml_scrape_svc(e.c, s.cluster.cluster, len(stacks))
        "cluster": s.cluster.cluster
        "c": e.c
    } for s in stacks for e in _ml_entries(s) if _ml_scrape_svc(e.c, s.cluster.cluster, len(stacks))]
}

# The Telemetry each scraped component lowers to, when the env's
# observability is on (else none). `taken` are the service names the env's
# own Telemetry declares: one of those replaces the derived scrape.
_ml_telemetry = lambda stacks: [any], obs_on: bool, taken: [str] -> [Telemetry] {
    [Telemetry {
        service_name = x.svc
        scope = "platform"
        cluster = x.cluster
        namespaces = [_ml_scrape_ns(x.c)] if _ml_scrape_ns(x.c) else []
        selector = x.c.selector
        metrics = [MetricsScrape {
            port = x.c.port
            allow = "|".join(x.c.allow) if x.c.allow else Undefined
        }]
    } for x in _ml_scrapes(stacks) if x.svc not in taken] if obs_on else []
}

# The HelmChart each Install lowers to, on its stack's cluster: {kind, chart}.
_ml_install_charts = lambda stacks: [any] -> [any] {
    [{
        "kind": e.c.kind
        "chart": e.c.chart if e.c.chart.cluster else e.c.chart | {cluster = s.cluster}
    } for s in stacks for e in _ml_entries(s) if typeof(e.c) == "Install"]
}

# The Namespaces the env's bundle Installs need, one per (cluster,
# namespace): forge creates none for a bundle chart. `rendered` are the
# "<cluster>/<namespace>" pairs the env already renders or declares, and
# Kubernetes' own namespaces are never rendered. Labelled with the component
# owner and never pruned: a namespace may hold more than the chart, and
# deleting it deletes everything in it.
_ml_namespaces = lambda stacks: [any], owner: str, rendered: [str] -> [any] {
    _want = {"${_eso_context(x.chart)}/${x.chart.namespace}": [_eso_context(x.chart), x.chart.namespace] \
        for x in _ml_install_charts(stacks) if x.chart.delivery == "bundle"}
    [{
        apiVersion = "v1"
        kind = "Namespace"
        metadata = {
            name = p[1]
            labels = {
                "app.kubernetes.io/managed-by" = "forge"
                "forge.dev/component-owner" = owner
                "forge.dev/cluster" = p[0]
            }
            annotations = {"kustomize.toolkit.fluxcd.io/prune" = "disabled"}
        }
    } for k, p in _want if k not in rendered and p[1] not in _ML_SYSTEM_NAMESPACES]
}

# Namespaces Kubernetes owns: never rendered for an Install.
_ML_SYSTEM_NAMESPACES: [str] = ["default", "kube-system", "kube-public", "kube-node-lease"]

# The kube contexts the env's Installs apply to: targeted contexts.
_ml_install_contexts = lambda stacks: [any] -> [str] {
    [s.cluster.cluster for s in stacks if len([e for e in _ml_entries(s) if typeof(e.c) == "Install"]) > 0]
}

# One component, as output.ml_stacks[].components[] (see MLStack). svc is
# its scrape's service.name when the scrape renders, else "".
_ml_render_component = lambda e: any, svc: str -> any {
    _c = e.c
    _adopt = typeof(_c) == "Adopt"
    {
        "slot": e.slot
        "vendor": e.vendor
        "mode": "adopt" if _adopt else "install"
        "kind": _c.kind
        "namespace": (_c.namespace or "") if _adopt else _c.chart.namespace
        "version": (_c.version or "") if _adopt else _c.chart.version
        "selector": _c.selector or {}
        "port": _c.port or 0
        "allow": _c.allow
        "note": (_c.note or "") if _adopt else ""
        "service_name": svc
        "chart": "" if _adopt else _c.chart.name
        "reason": "" if _adopt else (_c.reason or "")
    }
}

# output.ml_stacks (see MLStack). A scrape renders when the env's
# observability is on: as the derived Telemetry, or as the env's own
# Telemetry of that service name, which replaces it.
_ml_render = lambda stacks: [any], obs_on: bool -> [any] {
    [{
        "cluster": s.cluster.cluster
        "namespace": s.cluster.namespace
        "observed": s.observed
        "observed_at": s.observed_at or ""
        "accelerators": [{"vendor": a.vendor, "resource": a.resource, "node_selector": a.node_selector or {}} for a in s.accelerators]
        "components": [_ml_render_component(e, _ml_scrape_svc(e.c, s.cluster.cluster, len(stacks)) if obs_on else "") for e in _ml_entries(s)]
        "decisions": [{"id": d.id, "decision": d.decision, "reason": d.reason, "expires": d.expires} for d in s.decisions]
    } for s in stacks]
}
