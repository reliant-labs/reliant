"""A scraped ML-only cluster gets an agent and its destination credential."""
import forge

_target = forge.ClusterTarget {cluster = "gpu", namespace = "forge-ml", connected_cluster = "gpu-connected"}
_stack = forge.MLStack {
    cluster = _target
    monitoring = [forge.Adopt {kind = "prometheus", namespace = "monitoring", selector = {app = "prometheus"}, port = 9090}]
}
_bundle = forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com", token_from_control_plane = True}
    }
    ml_stacks = [_stack]
}
_out = forge.render(_bundle)
_agents = [m for m in _out.manifests if m.kind == "ObservabilityAgent"]
assert_agent = len(_agents) == 1 and _agents[0].metadata.labels["forge.dev/cluster"] == "gpu"
assert_scrape = len(_agents[0].spec.telemetry) == 1 and _agents[0].spec.telemetry[0].service_name == "ml-prometheus"
assert_credential = len(_out.control_plane_secrets) == 1 and _out.control_plane_secrets[0].cluster == "gpu" and _out.control_plane_secrets[0].connected_cluster == "gpu-connected"

_external = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        telemetry_collector = forge.TelemetryCollector {namespace = "otel", pod_labels = {app = "otel"}}
    }
    ml_stacks = [_stack]
})
assert_external_owns_scrape = len(_external.telemetry) == 1 and len([m for m in _external.manifests if m.kind == "ObservabilityAgent"]) == 0
assert_external_no_credential = len(_external.control_plane_secrets) == 0
