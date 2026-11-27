"""
Positive test — a forge.MLStack lowers through existing delivery.

WHAT THIS PINS
  * an Install becomes a `bundled_charts` entry on the stack's cluster (the
    ClusterTarget's context), carrying the labels forge stamps on every object
    the chart renders: forge.dev/component-owner = <project>.<env> and
    forge.dev/component = ml-<kind>;
  * a scraped Adopt (selector + port) becomes a `telemetry` target with the
    Observability.telemetry semantics: service ml-<kind>, scope platform, the
    stack's cluster, [namespace], the selector, one scrape keeping `allow`;
  * an Adopt without a port renders no target;
  * the stack's cluster is a targeted context, so it rides the agent record of
    that cluster like any declared telemetry;
  * `output.ml_stacks` projects the declared stack: every component with its
    slot, mode and kind, `observed`, the accelerators and the decisions;
  * Accelerators.resource defaults from the vendor.
"""

import forge
import forge.workloads as fw

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml", platform = "amd64"}

_stack = forge.MLStack {
    cluster = _gpu
    observed = ["gpu-operator", "dcgm-exporter", "kueue"]
    observed_at = "2026-11-25"
    accelerators = [forge.Accelerators {
        vendor = "nvidia"
        metrics = forge.Adopt {
            kind = "dcgm-exporter", namespace = "gpu-operator", version = "3.3.9"
            selector = {"app": "nvidia-dcgm-exporter"}, port = 9400
            allow = ["DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED"]
        }
    }]
    node_health = forge.Install {
        kind = "node-problem-detector"
        chart = forge.HelmChart {
            name = "node-problem-detector", repo = "https://charts.deliveryhero.io/"
            chart = "node-problem-detector", version = "2.3.14", namespace = "forge-ml", delivery = "bundle"
            values = {"metrics": {"enabled": True}}
        }
    }
    queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}
    training = [forge.Adopt {kind = "jobset"}]
    serving = [forge.Adopt {kind = "kserve"}]
    monitoring = [forge.Adopt {kind = "kube-state-metrics", namespace = "monitoring"}]
    decisions = [forge.RecommendationDecision {
        id = "kubelet-tracing-off", decision = "dismiss"
        reason = "GKE does not expose kubelet tracing config", expires = "2027-06-01"
    }]
}

_bundle = lambda obs: bool -> forge.Bundle {
    forge.Bundle {
        project = "acme"
        env = "prod"
        if obs:
            observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
        workloads = [fw.Workload {name = "api", image = "registry.example.com/acme/api:1", runtime = forge.OnCluster {target = _gpu}}]
        ml_stacks = [_stack]
    }
}

_out = forge.render(_bundle(True))
_dark = forge.render(_bundle(False))

# Install -> a bundle chart on the stack's cluster, labelled as forge's.
_charts = _out.bundled_charts
assert_one_chart = len(_charts) == 1 and _charts[0].name == "node-problem-detector"
assert_chart_pinned = _charts[0].repo == "https://charts.deliveryhero.io/" and _charts[0].chart == "node-problem-detector" and _charts[0].version == "2.3.14"
assert_chart_on_stack_cluster = _charts[0].cluster == "gke_acme_us-central1_gpu"
assert_chart_namespace = _charts[0].namespace == "forge-ml"
assert_chart_values = _charts[0].values == {"metrics": {"enabled": True}}
assert_chart_labels = _charts[0].labels == {"forge.dev/component-owner": "acme.prod", "forge.dev/component": "ml-node-problem-detector"}
assert_chart_not_bootstrap = len(_out.helm_charts) == 0

# Adopt with selector + port -> one telemetry target; the rest render none.
_tel = [t for t in _out.telemetry if t.source.startswith("ml-stack:")]
assert_one_scrape = len(_tel) == 1
assert_scrape_identity = _tel[0].service_name == "ml-dcgm-exporter" and _tel[0].scope == "platform" and _tel[0].attachment == "observability"
assert_scrape_source = _tel[0].source == "ml-stack:gke_acme_us-central1_gpu"
assert_scrape_target = _tel[0].runtime == "cluster" and _tel[0].cluster == "gke_acme_us-central1_gpu" and _tel[0].namespaces == ["gpu-operator"] and _tel[0].selector == {"app": "nvidia-dcgm-exporter"}
assert_scrape_port_allow = len(_tel[0].metrics) == 1 and _tel[0].metrics[0].port == 9400 and _tel[0].metrics[0].allow == "DCGM_FI_DEV_GPU_UTIL|DCGM_FI_DEV_FB_USED"
assert_scrape_on_agent = any m in _out.manifests { m.kind == "ObservabilityAgent" and "telemetry" in m.spec and any t in m.spec.telemetry { t.service_name == "ml-dcgm-exporter" } }

# Without observability there is nothing to scrape into.
assert_no_scrape_without_observability = "telemetry" not in _dark
assert_chart_without_observability = len(_dark.bundled_charts) == 1

# output.ml_stacks: the declared stack, read by tools.
_m = _out.ml_stacks[0]
assert_ml_one_stack = len(_out.ml_stacks) == 1
assert_ml_cluster = _m.cluster == "gke_acme_us-central1_gpu" and _m.namespace == "forge-ml"
assert_ml_observed = _m.observed == ["gpu-operator", "dcgm-exporter", "kueue"] and _m.observed_at == "2026-11-25"
assert_ml_accelerators = _m.accelerators == [{"vendor": "nvidia", "resource": "nvidia.com/gpu", "node_selector": {}}]
assert_ml_slots = [[c.slot, c.mode, c.kind] for c in _m.components] == [
    ["accelerators", "adopt", "dcgm-exporter"]
    ["node_health", "install", "node-problem-detector"]
    ["queue", "adopt", "kueue"]
    ["training", "adopt", "jobset"]
    ["serving", "adopt", "kserve"]
    ["monitoring", "adopt", "kube-state-metrics"]
]
assert_ml_adopt_detail = _m.components[0] == {
    "slot": "accelerators", "vendor": "nvidia", "mode": "adopt", "kind": "dcgm-exporter", "namespace": "gpu-operator"
    "version": "3.3.9", "selector": {"app": "nvidia-dcgm-exporter"}, "port": 9400
    "allow": ["DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED"], "note": "", "service_name": "ml-dcgm-exporter"
    "chart": "", "reason": ""
}
assert_ml_install_detail = _m.components[1].chart == "node-problem-detector" and _m.components[1].version == "2.3.14" and _m.components[1].namespace == "forge-ml" and _m.components[1].vendor == ""
assert_ml_unscraped_has_no_service = _m.components[2].service_name == "" and _m.components[2].port == 0
assert_ml_no_service_without_observability = _dark.ml_stacks[0].components[0].service_name == ""
assert_ml_decisions = _m.decisions == [{"id": "kubelet-tracing-off", "decision": "dismiss", "reason": "GKE does not expose kubelet tracing config", "expires": "2027-06-01"}]

# Accelerator resources default by vendor; generic names its own.
assert_resource_defaults = [forge.Accelerators {vendor = v}.resource for v in ["nvidia", "amd", "tpu", "neuron", "gaudi"]] == ["nvidia.com/gpu", "amd.com/gpu", "google.com/tpu", "aws.amazon.com/neuroncore", "habana.ai/gaudi"]
assert_resource_generic = forge.Accelerators {vendor = "generic", resource = "example.com/npu"}.resource == "example.com/npu"
