"""
Positive test — declared telemetry rides forge's node agent.

The agent record of a cluster carries that cluster's Telemetry targets (and
only that cluster's: a host target and another cluster's stay off it), so Go
renders them into the agent's config. A credential the agent mounts resolves
through required_secrets in the agent's namespace. An env with no Telemetry
renders the record with no `telemetry` key, and an env that names its own
collector (Observability.telemetry_collector) moves every target off the
agent and projects that collector for the NetworkPolicy rules.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

_bundle = lambda telemetry: [forge.Telemetry], own: any -> forge.Bundle {
    forge.Bundle {
        project = "demo"
        env = "staging"
        required_secrets = [
            forge.ExternalSecret {name = "otlp-auth", namespace = "forge-observability", keys = ["headers"]}
            forge.ExternalSecret {name = "openbao-metrics", namespace = "forge-observability", keys = ["token"]}
        ]
        observability = forge.Observability {
            destination = forge.ObservabilityDestination {
                endpoint = "https://otlp.example.com"
                headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
            }
            telemetry = telemetry
            if own:
                telemetry_collector = own
        }
        workloads = [fw.Workload {
            name = "zitadel", image = "ghcr.io/zitadel/zitadel:v4", ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _k3d}
            telemetry = forge.Telemetry {metrics = [forge.MetricsScrape {port = "http", path = "/debug/metrics"}]}
        }]
    }
}

_declared = [
    forge.Telemetry {
        service_name = "openbao", cluster = "k3d-demo", namespaces = ["openbao"], selector = {"app.kubernetes.io/name" = "openbao"}
        metrics = [forge.MetricsScrape {port = 8200, path = "/v1/sys/metrics", scheme = "https", bearer_token = forge.SecretRef {name = "openbao-metrics", key = "token"}}]
    }
    forge.Telemetry {service_name = "other", cluster = "k3d-other", selector = {app = "other"}, checks = [forge.HealthCheck {port = 80}]}
    forge.Telemetry {service_name = "devbox", address = "127.0.0.1", checks = [forge.HealthCheck {port = 22}]}
]

_with = forge.render(_bundle(_declared, None))
_own = forge.render(_bundle(_declared, forge.TelemetryCollector {namespace = "observability", pod_labels = {app = "otel"}}))

_agent = lambda out: any -> any {
    [m for m in out.manifests if m.kind == "ObservabilityAgent"][0]
}

assert_agent_carries_its_cluster_targets = [t.service_name for t in _agent(_with).spec.telemetry] == ["zitadel", "openbao"]
assert_agent_targets_are_the_rendered_ones = _agent(_with).spec.telemetry[1] == [t for t in _with.telemetry if t.service_name == "openbao"][0]
assert_no_collector_projected_without_one = "telemetry_collector" not in _with
assert_own_collector_takes_the_targets = "telemetry" not in _agent(_own).spec
assert_own_collector_projected = _own.telemetry_collector == {"namespace": "observability", "pod_labels": {"app": "otel"}}

_none = forge.render(forge.Bundle {
    project = "demo"
    required_secrets = [forge.ExternalSecret {name = "otlp-auth", namespace = "forge-observability", keys = ["headers"]}]
    observability = forge.Observability {destination = forge.ObservabilityDestination {
        endpoint = "https://otlp.example.com"
        headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
    }}
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})

assert_absent_without_telemetry = "telemetry" not in _agent(_none).spec and "telemetry_collector" not in _none
