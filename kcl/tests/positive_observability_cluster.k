"""
Positive test — a cluster env with a declared destination.

A cluster workload exports to the node-local agent Service, the env gets ONE
ObservabilityAgent record per cluster (Go expands it to the DaemonSet), the
NetworkPolicy telemetry allow follows the agent, and a hosted workload gets
only the protocol: the platform owns its endpoint. An off signal is switched
off at the app. The headers secret resolves through required_secrets.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "staging"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    network_policy = forge.NetworkPolicy {}
    required_secrets = [forge.ExternalSecret {name = "otlp-auth", namespace = "forge-observability", keys = ["headers"]}]
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
        }
        signals = forge.ObservabilitySignals {metrics = False}
        sampling = forge.ObservabilitySampling {traces_ratio = 0.1}
    }
    workloads = [
        fw.Workload {name = "api", image = "localhost:5050/demo", build = _go, args = ["api"], runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "web", image = "ghcr.io/acme/web", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}
    ]
})

_env = lambda name: str -> {str:str} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == name][0].spec.env or []) if e.value}
}
_agents = [m for m in output.manifests if m.kind == "ObservabilityAgent"]

assert_cluster_exports_to_node_agent = _env("api").OTEL_EXPORTER_OTLP_ENDPOINT == "http://otel-agent.forge-observability.svc.cluster.local:4318" and _env("api").OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf"
assert_off_signal_switched_off_at_the_app = _env("api").OTEL_METRICS_EXPORTER == "none" and "OTEL_TRACES_EXPORTER" not in _env("api")
assert_hosted_gets_no_endpoint = "OTEL_EXPORTER_OTLP_ENDPOINT" not in _env("web") and _env("web").OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf"
assert_one_agent_record = len(_agents) == 1
assert_agent_record_carries_the_declaration = _agents[0].spec.endpoint == "https://otlp.example.com" and _agents[0].spec.headersSecret.name == "otlp-auth" and _agents[0].spec.signals.metrics == False
assert_agent_record_routed_to_its_cluster = _agents[0].metadata.labels["forge.dev/cluster"] == "k3d-demo" and _agents[0].metadata.namespace == "forge-observability"
assert_telemetry_netpol_follows_the_agent = output.network_policy.telemetry_namespace == "forge-observability"
assert_declaration_projected = output.observability.destination.endpoint == "https://otlp.example.com" and output.observability.sampling.traces_ratio == 0.1
# The ratio is HEAD sampling in the SDK, for every runtime that runs a process:
# the sampler is a pair, a trace is whole or absent, and the agent samples nothing.
assert_ratio_is_head_sampled_on_cluster = _env("api").OTEL_TRACES_SAMPLER == "parentbased_traceidratio" and _env("api").OTEL_TRACES_SAMPLER_ARG == "0.1"
assert_agent_record_carries_no_sampling = "samplingRatio" not in _agents[0].spec and "errorsAlways" not in _agents[0].spec
