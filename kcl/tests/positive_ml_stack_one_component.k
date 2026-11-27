"""
Positive test — a stack of one component is valid and renders only that
component.

Nothing about an MLStack is all or nothing: only `cluster` is required, every
slot is optional, and a stack declares any subset of components. Each stack
here declares exactly one, and its render is the no-stack render plus that
component's own output and nothing for any kind it does not declare:

  * Kueue installed alone (delivery "bootstrap", the default): one
    `helm_charts` entry, labelled as forge's; no `bundled_charts`, no
    Namespace of forge's, no scrape;
  * Kueue adopted alone (no port): nothing in the cluster, only the
    `ml_stacks` projection;
  * the DCGM exporter adopted alone, scraped: one `ml-stack:` telemetry
    target; no chart of either delivery, no Namespace.
"""

import json
import forge
import forge.workloads as fw

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "acme-prod", platform = "amd64"}

_base = lambda stacks: [forge.MLStack] -> forge.Bundle {
    forge.Bundle {
        project = "acme"
        env = "prod"
        observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
        workloads = [fw.Workload {name = "api", image = "registry.example.com/acme/api:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _gpu}}]
        ml_stacks = stacks
    }
}

_without = forge.render(_base([]))

# The render with the named keys dropped: what is left must be the no-stack render.
_drop = lambda out: any, keys: [str] -> any {
    {k: v for k, v in out if k not in keys}
}
_namespaces = lambda out: any -> [str] {
    [m.metadata.name for m in out.manifests if m.kind == "Namespace"]
}
_ml_scrapes = lambda out: any -> [str] {
    [t.service_name for t in out.telemetry if t.source.startswith("ml-stack:")] if "telemetry" in out else []
}

# Kueue installed alone.
_kueue_install = forge.render(_base([forge.MLStack {
    cluster = _gpu
    queue = forge.Install {
        kind = "kueue"
        chart = forge.HelmChart {name = "kueue", oci = "oci://registry.k8s.io/kueue/charts/kueue", version = "0.14.4", namespace = "kueue-system"}
    }
}]))
assert_kueue_install_one_chart = [h.name for h in _kueue_install.helm_charts] == ["kueue"]
assert_kueue_install_chart_on_stack_cluster = _kueue_install.helm_charts[0].cluster == "gke_acme_us-central1_gpu" and _kueue_install.helm_charts[0].cluster_target == True and _kueue_install.helm_charts[0].namespace == "kueue-system"
assert_kueue_install_labelled = _kueue_install.helm_charts[0].labels == {"forge.dev/component-owner": "acme.prod", "forge.dev/component": "ml-kueue"}
assert_kueue_install_nothing_bundled = "bundled_charts" not in _kueue_install
assert_kueue_install_no_namespace = _namespaces(_kueue_install) == _namespaces(_without)
assert_kueue_install_no_scrape = _ml_scrapes(_kueue_install) == []
assert_kueue_install_only_its_chart = json.encode(_drop(_kueue_install, ["ml_stacks", "helm_charts"])) == json.encode(_drop(_without, ["helm_charts"]))
assert_kueue_install_projected = [[c.slot, c.mode, c.kind] for c in _kueue_install.ml_stacks[0].components] == [["queue", "install", "kueue"]]

# Kueue adopted alone, unscraped: an inventory assertion, nothing in the cluster.
_kueue_adopt = forge.render(_base([forge.MLStack {cluster = _gpu, queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}}]))
assert_kueue_adopt_lowers_to_nothing = json.encode(_drop(_kueue_adopt, ["ml_stacks"])) == json.encode(_without)
assert_kueue_adopt_projected = [[c.slot, c.mode, c.kind] for c in _kueue_adopt.ml_stacks[0].components] == [["queue", "adopt", "kueue"]]

# The DCGM exporter adopted alone, scraped.
_dcgm = forge.render(_base([forge.MLStack {
    cluster = _gpu
    accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Adopt {
        kind = "dcgm-exporter", namespace = "gpu-operator"
        selector = {"app": "nvidia-dcgm-exporter"}, port = 9400, allow = ["DCGM_FI_DEV_GPU_UTIL"]
    }}]
}]))
assert_dcgm_one_scrape = _ml_scrapes(_dcgm) == ["ml-dcgm-exporter"]
_dcgm_tel = [t for t in _dcgm.telemetry if t.source.startswith("ml-stack:")][0]
assert_dcgm_scrape_target = _dcgm_tel.cluster == "gke_acme_us-central1_gpu" and _dcgm_tel.namespaces == ["gpu-operator"] and _dcgm_tel.selector == {"app": "nvidia-dcgm-exporter"} and _dcgm_tel.metrics[0].port == 9400
assert_dcgm_no_chart = "bundled_charts" not in _dcgm and json.encode(_dcgm.helm_charts if "helm_charts" in _dcgm else []) == json.encode(_without.helm_charts if "helm_charts" in _without else [])
assert_dcgm_no_namespace = _namespaces(_dcgm) == _namespaces(_without)
assert_dcgm_other_telemetry_unchanged = json.encode([t for t in _dcgm.telemetry if not t.source.startswith("ml-stack:")]) == json.encode(_without.telemetry if "telemetry" in _without else [])
assert_dcgm_projected = [[c.slot, c.vendor, c.mode, c.kind] for c in _dcgm.ml_stacks[0].components] == [["accelerators", "nvidia", "adopt", "dcgm-exporter"]]
