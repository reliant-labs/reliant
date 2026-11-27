"""
Positive test — an Install lowers by its chart's delivery, labelled as
forge's on both paths, and an installed exporter is scraped like an adopted one.

WHAT THIS PINS
  * delivery "bootstrap" (an operator with CRDs, e.g. JobSet): a `helm_charts`
    entry on the stack's ClusterTarget (`cluster` its context,
    `cluster_target` true), with the labels Go stamps on every object, and
    no `bundled_charts` entry;
  * delivery "bundle": a `bundled_charts` entry, and the Namespace forge
    creates for no bundle chart, rendered once per (cluster, namespace) with
    the component owner and Flux's prune switched off;
  * that Namespace is not rendered when the env renders it already (a
    workload namespace) or declares it in a forge.Manifests group, nor for a
    namespace Kubernetes owns;
  * an Install with selector + port renders a scrape with
    namespaces = [chart.namespace].
"""

import forge
import forge.workloads as fw

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "acme-prod", platform = "amd64"}

_chart = lambda name: str, ns: str, delivery: str -> forge.HelmChart {
    forge.HelmChart {name = name, repo = "https://charts.example.com/", chart = name, version = "1.0.0", namespace = ns, delivery = delivery}
}

_bundle = lambda stack: forge.MLStack, manifests: [forge.Manifests] -> forge.Bundle {
    forge.Bundle {
        project = "acme"
        env = "prod"
        observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
        workloads = [fw.Workload {name = "api", image = "registry.example.com/acme/api:1", runtime = forge.OnCluster {target = _gpu}}]
        manifests = manifests
        ml_stacks = [stack]
    }
}

_out = forge.render(_bundle(forge.MLStack {
    cluster = _gpu
    accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Install {
        kind = "dcgm-exporter", chart = _chart("dcgm-exporter", "forge-ml", "bundle")
        selector = {"app.kubernetes.io/name": "dcgm-exporter"}, port = 9400, allow = ["DCGM_FI_DEV_GPU_UTIL"]
    }}]
    node_health = forge.Install {kind = "node-problem-detector", chart = _chart("node-problem-detector", "forge-ml", "bundle")}
    pull_cache = forge.Install {kind = "spegel", chart = _chart("spegel", "kube-system", "bundle")}
    monitoring = [forge.Install {kind = "kube-state-metrics", chart = _chart("kube-state-metrics", "acme-prod", "bundle")}]
    training = [forge.Install {kind = "jobset", chart = _chart("jobset", "jobset-system", "bootstrap")}]
}, []))

# bootstrap -> helm_charts on the ClusterTarget, labelled.
_boot = [h for h in _out.helm_charts if h.name == "jobset"]
assert_bootstrap_chart = len(_boot) == 1 and _boot[0].cluster == "gke_acme_us-central1_gpu" and _boot[0].cluster_target == True and _boot[0].namespace == "jobset-system"
assert_bootstrap_labels = _boot[0].labels == {"forge.dev/component-owner": "acme.prod", "forge.dev/component": "ml-jobset"}
assert_bootstrap_not_bundled = "jobset" not in [c.name for c in _out.bundled_charts]
assert_bundle_charts = [c.name for c in _out.bundled_charts] == ["dcgm-exporter", "node-problem-detector", "kube-state-metrics", "spegel"]

# bundle -> one Namespace per (cluster, namespace) forge does not render already.
_ns = [m for m in _out.manifests if m.kind == "Namespace" and "forge.dev/component-owner" in m.metadata.labels]
assert_one_ml_namespace = [m.metadata.name for m in _ns] == ["forge-ml"]
assert_ml_namespace_meta = _ns[0].metadata.labels["forge.dev/component-owner"] == "acme.prod" and _ns[0].metadata.labels["forge.dev/cluster"] == "gke_acme_us-central1_gpu" and _ns[0].metadata.annotations["kustomize.toolkit.fluxcd.io/prune"] == "disabled"
assert_workload_namespace_once = len([m for m in _out.manifests if m.kind == "Namespace" and m.metadata.name == "acme-prod"]) == 1
assert_no_system_namespace = len([m for m in _out.manifests if m.kind == "Namespace" and m.metadata.name == "kube-system"]) == 0
assert_no_namespace_for_bootstrap = len([m for m in _out.manifests if m.kind == "Namespace" and m.metadata.name == "jobset-system"]) == 0

# An Install with selector + port is scraped in its chart's namespace.
_tel = [t for t in _out.telemetry if t.source.startswith("ml-stack:")]
assert_install_scraped = [t.service_name for t in _tel] == ["ml-dcgm-exporter"] and _tel[0].namespaces == ["forge-ml"] and _tel[0].metrics[0].allow == "DCGM_FI_DEV_GPU_UTIL"
assert_install_scrape_projected = _out.ml_stacks[0].components[0].service_name == "ml-dcgm-exporter" and _out.ml_stacks[0].components[0].port == 9400 and _out.ml_stacks[0].components[0].mode == "install"

# A Namespace the env declares itself is not rendered twice.
_declared = forge.render(_bundle(forge.MLStack {
    cluster = _gpu
    node_health = forge.Install {kind = "node-problem-detector", chart = _chart("node-problem-detector", "forge-ml", "bundle")}
}, [forge.Manifests {name = "ml-ns", cluster = "gke_acme_us-central1_gpu", objects = [{apiVersion = "v1", kind = "Namespace", metadata = {name = "forge-ml", labels = {"pod-security.kubernetes.io/enforce" = "privileged"}}}]}]))
assert_declared_namespace_once = len([m for m in _declared.manifests if m.kind == "Namespace" and m.metadata.name == "forge-ml"]) == 1
