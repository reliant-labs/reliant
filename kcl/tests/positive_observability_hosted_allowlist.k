"""
Positive test — an env that is hosted-only and declares observability still
renders: the declaration adds only the standard OTEL_* contract to a hosted
workload, which the Restricted profile's env allowlist accepts, and forge
renders no agent for it (the platform's operator does).
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        signals = forge.ObservabilitySignals {logs = False}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]
})

_env = {e.name: e.value for w in output.workloads if w.name == "api" for e in w.spec.env if e.value}
assert_only_protocol_and_switches = _env.OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf" and "OTEL_EXPORTER_OTLP_ENDPOINT" not in _env and _env.OTEL_LOGS_EXPORTER == "none"
assert_no_agent_for_hosted = len([m for m in output.manifests if m.kind == "ObservabilityAgent"]) == 0
