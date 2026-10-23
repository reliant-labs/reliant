"""
Positive test — profiles follow where the store is. Unset `signals.profiles`
is ON with the local ClickStack (no destination, a `profiles_port`): host
processes get the full profiles URL of the local profiles collector. It is OFF
with a destination, on forge.OnHosted and without a profiles collector
(OTEL_PROFILES_EXPORTER=none). An explicit value wins either way, and an
author's own transport or profiles switch wins over the derived one.
"""

import forge
import forge.workloads as fw

_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_render = lambda o: forge.Observability, rt: any -> any {
    forge.render(forge.Bundle {
        project = "demo"
        control_plane = forge.ControlPlane {} if rt.type == "hosted" else None
        observability = o
        workloads = [
            fw.Workload {name = "api", build = _go, args = ["api"], runtime = rt}
            fw.Workload {name = "own", build = _go, args = ["own"], env = {OTEL_EXPORTER_OTLP_PROTOCOL = "grpc"}, runtime = rt}
            fw.Workload {name = "mine", build = _go, args = ["mine"], env = {OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT = "http://p:1/x", OTEL_PROFILES_EXPORTER = "otlp"}, runtime = rt}
        ]
    })
}

_env = lambda out: any, name: str -> {str:str} {
    {e.name: e.value for e in ([w for w in out.workloads if w.name == name][0].spec.env or []) if e.value}
}

_local = _render(forge.Observability {local_port = 4318, profiles_port = 4328}, forge.OnHost {})
_local_off = _render(forge.Observability {local_port = 4318, profiles_port = 4328, signals = forge.ObservabilitySignals {profiles = False}}, forge.OnHost {})
_no_collector = _render(forge.Observability {local_port = 4318}, forge.OnHost {})
_dest_default = _render(forge.Observability {local_port = 4318, destination = _dest}, forge.OnHost {})
_dest_on = _render(forge.Observability {local_port = 4318, destination = _dest, signals = forge.ObservabilitySignals {profiles = True}}, forge.OnHost {})
_hosted = _render(forge.Observability {}, forge.OnHosted {})

assert_local_default_on = _env(_local, "api").OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT == "http://127.0.0.1:4328/v1development/profiles" and "OTEL_PROFILES_EXPORTER" not in _env(_local, "api")
assert_local_projected = _local.observability.signals.profiles == True and _local.observability.profiles_endpoint == "http://127.0.0.1:4328/v1development/profiles"
assert_author_transport_takes_profiles_along = "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT" not in _env(_local, "own")
assert_author_profiles_endpoint_wins = _env(_local, "mine").OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT == "http://p:1/x"
assert_local_explicit_off = _env(_local_off, "api").OTEL_PROFILES_EXPORTER == "none" and "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT" not in _env(_local_off, "api") and _local_off.observability.signals.profiles == False
assert_author_switch_wins = _env(_local_off, "mine").OTEL_PROFILES_EXPORTER == "otlp"
assert_no_collector_off = _env(_no_collector, "api").OTEL_PROFILES_EXPORTER == "none" and _no_collector.observability.profiles_endpoint == None
assert_destination_default_off = _env(_dest_default, "api").OTEL_PROFILES_EXPORTER == "none" and _dest_default.observability.signals.profiles == False
assert_destination_explicit_on = "OTEL_PROFILES_EXPORTER" not in _env(_dest_on, "api") and "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT" not in _env(_dest_on, "api") and _dest_on.observability.signals.profiles == True
assert_hosted_default_off = _env(_hosted, "api").OTEL_PROFILES_EXPORTER == "none"
