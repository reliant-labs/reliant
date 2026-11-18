"""
Positive test — the dev default: a Bundle declaring only the local collector's
loopback port. Host processes get the endpoint + protocol as a PAIR, the
service/resource keys stay derived, an author's own transport wins whole, and
no destination means no agent record is needed for a host-only env.
"""

import forge
import forge.workloads as fw

_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318}
    workloads = [
        fw.Workload {name = "api", build = _go, args = ["api"], runtime = forge.OnHost {}}
        fw.Workload {name = "own", build = _go, args = ["own"], env = {OTEL_EXPORTER_OTLP_ENDPOINT = "http://collector:4317"}, runtime = forge.OnHost {}}
        fw.Workload {name = "blank", build = _go, args = ["blank"], env = {OTEL_EXPORTER_OTLP_ENDPOINT = ""}, runtime = forge.OnHost {}}
        fw.Workload {name = "cli", kind = "tool", build = _go, args = ["admin"], runtime = forge.OnHost {}}
    ]
})

_env = lambda name: str -> {str:str} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == name][0].spec.env or []) if e.value}
}

assert_endpoint_and_protocol_paired = _env("api").OTEL_EXPORTER_OTLP_ENDPOINT == "http://127.0.0.1:4318" and _env("api").OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf"
assert_service_name_still_derived = _env("api").OTEL_SERVICE_NAME == "api"
assert_author_transport_wins_whole = _env("own").OTEL_EXPORTER_OTLP_ENDPOINT == "http://collector:4317" and "OTEL_EXPORTER_OTLP_PROTOCOL" not in _env("own")
assert_blank_endpoint_takes_derived = _env("blank").OTEL_EXPORTER_OTLP_ENDPOINT == "http://127.0.0.1:4318"
assert_tool_runs_nothing = "OTEL_EXPORTER_OTLP_ENDPOINT" not in _env("cli")
assert_logs_exporter_left_unset = "OTEL_LOGS_EXPORTER" not in _env("api")
assert_declaration_projected = output.observability.local_endpoint == "http://127.0.0.1:4318" and output.observability.destination == None
assert_no_agent_without_cluster_workloads = len([m for m in output.manifests if m.kind == "ObservabilityAgent"]) == 0
