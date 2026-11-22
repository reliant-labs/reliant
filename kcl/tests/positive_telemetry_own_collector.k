"""
Positive test — a project that runs its OWN collector (a hand-rolled
DaemonSet agent, declared as Observability.telemetry_collector) renders the
collector config forge would, from the same declarations:

  * forge.telemetry_targets(bundle) is the render's `output.telemetry`
  * forge.telemetry_collector(targets, opts) keeps the targets this collector
    reaches (the hub cluster's; not the host address, not the other cluster)
    and returns whole pipelines to merge plus the pieces to splice: the
    agent's own logs pipeline takes logs.processors after k8sattributes,
    its filelog takes logs.preserve, k8sattributes takes the selector labels
  * an isolated target exports through the isolated exporter only
  * the NetworkPolicy of the target that asks for one admits the declared
    collector, and joins Bundle.manifests beside the agent

internal/kclplugin's TestTelemetryCollectorPluginMatchesBuild renders this
file and holds `fragment` byte-equal to pkg/telemetry/collector.Build over
`targets`.
"""

import forge
import yaml

_hub = "k3d-hub"
_collector = forge.TelemetryCollector {namespace = "observability", pod_labels = {"app.kubernetes.io/name" = "agent"}}

_bundle = forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry_collector = _collector
        telemetry = [
            forge.Telemetry {service_name = "devbox", address = "127.0.0.1", checks = [forge.HealthCheck {port = 22}]}
            forge.Telemetry {
                service_name = "bus", scope = "platform", cluster = _hub, address = "bus.messaging.svc", isolated = True
                metrics = [forge.MetricsScrape {port = 7777, allow = "bus_.*", counters = "bus_.*_total"}]
            }
            forge.Telemetry {service_name = "elsewhere", cluster = "k3d-other", selector = {app = "x"}, metrics = [forge.MetricsScrape {port = 8080}]}
        ]
    }
    manifests = [forge.Manifests {name = "engine", namespace = "engine", cluster = _hub, telemetry = [forge.Telemetry {
        service_name = "engine"
        scope = "platform"
        selector = {app = "engine"}
        network_policy = True
        metrics = [forge.MetricsScrape {port = 9090, allow = "engine_.*", drop_labels = ["instance_id"]}]
        logs = forge.LogParse {format = "json", severity = forge.LogSeverity {field = "level"}, body = "msg"}
    }]}]
}

targets = forge.telemetry_targets(_bundle)
fragment = forge.telemetry_collector(targets, forge.TelemetryCollectorOptions {
    runtime = "cluster"
    cluster = _hub
    discovery = "all"
    resource_attrs = {"deployment.environment.name" = "dev"}
    processors = ["memory_limiter"]
    post_processors = ["batch"]
    metrics_exporters = ["otlphttp/platform"]
    isolated = forge.TelemetryIsolatedExporter {name = "otlphttp/selfmon", config = {endpoint = "http://gateway.observability.svc:4318"}}
    collector = _bundle.observability.telemetry_collector
})

# The hand-rolled agent: its own pipelines, with the fragment merged in.
_own = {
    receivers = {filelog = {include = ["/var/log/pods/*/*/*.log"], operators = [{type = "container", id = "container"}] + ([fragment.logs.preserve] if fragment.logs.preserve else [])}, kubeletstats = {}}
    processors = {
        memory_limiter = {check_interval = "1s", limit_percentage = 80}
        batch = {}
        k8sattributes = {extract = {labels = fragment.k8sattributes_labels}}
    }
    exporters = {"otlphttp/platform" = {endpoint = "http://gateway.observability.svc:4318"}}
}
_config = {
    extensions = fragment.extensions
    receivers = _own.receivers | fragment.receivers
    processors = _own.processors | fragment.processors
    exporters = _own.exporters | fragment.exporters
    service = {
        extensions = fragment.service_extensions
        pipelines = {
            logs = {receivers = ["filelog"], processors = ["memory_limiter", "k8sattributes"] + fragment.logs.processors + ["batch"], exporters = ["otlphttp/platform"]}
            "metrics/node" = {receivers = ["kubeletstats"], processors = ["memory_limiter", "k8sattributes"] + fragment.node_metrics_processors + ["batch"], exporters = ["otlphttp/platform"]}
        } | fragment.pipelines
    }
}

_out = forge.render(_bundle | {manifests = _bundle.manifests + [forge.Manifests {
    name = "agent"
    namespace = "observability"
    cluster = _hub
    objects = [{
        apiVersion = "v1"
        kind = "ConfigMap"
        metadata = {name = "agent-config", namespace = "observability"}
        data = {"config.yaml" = yaml.encode(_config)}
    }] + fragment.network_policies
}]})

_pipelines = _config.service.pipelines

assert_targets_are_the_render = targets == _out.telemetry
assert_reached_only = sorted([p for p in fragment.pipelines]) == ["metrics/telemetry_bus", "metrics/telemetry_engine"]
assert_engine_pipeline = _pipelines["metrics/telemetry_engine"] == {
    receivers = ["prometheus/telemetry_engine"]
    processors = ["memory_limiter", "transform/telemetry_target/engine", "batch"]
    exporters = ["otlphttp/platform"]
}
assert_isolated_exporter = _pipelines["metrics/telemetry_bus"].exporters == ["otlphttp/selfmon"] and fragment.exporters["otlphttp/selfmon"].sending_queue.queue_size == 200
assert_logs_spliced = _pipelines.logs.processors == ["memory_limiter", "k8sattributes", "transform/telemetry_identity", "transform/telemetry_logs", "batch"]
assert_preserve_spliced = len(_config.receivers.filelog.operators) == 2
assert_selector_labels = len(fragment.k8sattributes_labels) == 1
assert_policy = [[p.metadata.namespace, p.spec.ingress[0].from[0].podSelector.matchLabels] for p in fragment.network_policies] == [["engine", {"app.kubernetes.io/name": "agent"}]]
assert_agent_rendered = len([m for m in _out.manifests if m.kind == "ConfigMap" and m.metadata.name == "agent-config"]) == 1
assert_policy_rendered = len([m for m in _out.manifests if m.kind == "NetworkPolicy" and m.metadata?.labels?["forge.dev/telemetry-service"] == "engine" and m.metadata.name.startswith("telemetry-engine-")]) == 1
