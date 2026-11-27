"""
Positive test — the service.name of an adopted component's scrape.

  * one stack: ml-<kind>;
  * several stacks: ml-<kind>-<cluster>, so two clusters' exporters stay apart;
  * Adopt.service_name names it outright (two scraped adoptions of one kind);
  * a Telemetry the env declares itself with the derived name replaces the
    derived scrape (how a scrape gets an interval or dashboards of its own),
    and the component still projects that service_name.
"""

import forge
import forge.workloads as fw

_a = forge.ClusterTarget {cluster = "gke_acme_us-central1_a", namespace = "forge-ml", platform = "amd64"}
_b = forge.ClusterTarget {cluster = "gke_acme_us-central1_b", namespace = "forge-ml", platform = "amd64"}

_dcgm = forge.Adopt {kind = "dcgm-exporter", namespace = "gpu-operator", selector = {"app": "nvidia-dcgm-exporter"}, port = 9400}

_bundle = lambda stacks: [forge.MLStack], own: [forge.Telemetry] -> forge.Bundle {
    forge.Bundle {
        project = "acme"
        env = "prod"
        observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}, telemetry = own}
        workloads = [fw.Workload {name = "api", image = "registry.example.com/acme/api:1", runtime = forge.OnCluster {target = _a}}]
        ml_stacks = stacks
    }
}

_ml = lambda out: any -> [str] {
    [t.service_name for t in out.telemetry if t.source.startswith("ml-stack:")]
}

_one = forge.render(_bundle([forge.MLStack {cluster = _a, accelerators = [forge.Accelerators {vendor = "nvidia", metrics = _dcgm}]}], []))
assert_one_stack_name = _ml(_one) == ["ml-dcgm-exporter"]

_two = forge.render(_bundle([
    forge.MLStack {cluster = _a, accelerators = [forge.Accelerators {vendor = "nvidia", metrics = _dcgm}]}
    forge.MLStack {cluster = _b, accelerators = [forge.Accelerators {vendor = "nvidia", metrics = _dcgm}]}
], []))
assert_two_stack_names = _ml(_two) == ["ml-dcgm-exporter-gke_acme_us-central1_a", "ml-dcgm-exporter-gke_acme_us-central1_b"]
assert_two_stack_clusters = [t.cluster for t in _two.telemetry if t.source.startswith("ml-stack:")] == ["gke_acme_us-central1_a", "gke_acme_us-central1_b"]
_agents = [m for m in _two.manifests if m.kind == "ObservabilityAgent"]
assert_one_agent_per_cluster = sorted([m.metadata.labels["forge.dev/cluster"] for m in _agents]) == [_a.cluster, _b.cluster]
assert_cluster_local_scrapes = all m in _agents { len(m.spec.telemetry) == 1 and m.spec.telemetry[0].cluster == m.metadata.labels["forge.dev/cluster"] }

_named = forge.render(_bundle([forge.MLStack {
    cluster = _a
    monitoring = [
        forge.Adopt {kind = "prometheus", namespace = "team-a", selector = {"app": "prometheus"}, port = 9090, service_name = "ml-prometheus-team-a"}
        forge.Adopt {kind = "prometheus", namespace = "team-b", selector = {"app": "prometheus"}, port = 9090, service_name = "ml-prometheus-team-b"}
    ]
}], []))
assert_explicit_names = _ml(_named) == ["ml-prometheus-team-a", "ml-prometheus-team-b"]

_replaced = forge.render(_bundle([forge.MLStack {cluster = _a, accelerators = [forge.Accelerators {vendor = "nvidia", metrics = _dcgm}]}], [
    forge.Telemetry {
        service_name = "ml-dcgm-exporter", cluster = "gke_acme_us-central1_a", selector = {"app": "nvidia-dcgm-exporter"}
        metrics = [forge.MetricsScrape {port = 9400, interval = "15s"}]
    }
]))
assert_replaced_by_own = _ml(_replaced) == [] and [t.metrics[0].interval for t in _replaced.telemetry if t.service_name == "ml-dcgm-exporter"] == ["15s"]
assert_replaced_still_projected = _replaced.ml_stacks[0].components[0].service_name == "ml-dcgm-exporter"
