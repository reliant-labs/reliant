"""
Positive test — a container-free host dev stack with hosted dev telemetry
(reliant's deploy/kcl/dev, minimized; 4bc4f56c).

Host processes, forge.HostInfra and a host frontend: nothing runs a
container, so there is no local collector and no `local_port` to declare.
`dev_telemetry` decides alone — "auto" ships to the hosted store when signed
in (forge env up injects the hosted endpoint then), and the local fallback,
like a declared "local", exports nothing: no endpoint is rendered, so no
process retries a dead loopback port. Trace context still propagates.
"""

import forge
import forge.workloads as fw

_go = forge.GoBuild {cmd = "./cmd/reliant", output_name = "reliant"}

_bundle = lambda dev_telemetry: str -> forge.Bundle {
    forge.Bundle {
        project = "reliant"
        lifecycle = "local"
        control_plane = forge.ControlPlane {}
        observability = forge.Observability {
            dev_telemetry = dev_telemetry
            retention = forge.ObservabilityRetention {
                logs_days = 3
                overrides = [forge.RetentionOverride {service = "reliant-temporal-worker", signal = "logs", days = 1}]
            }
        }
        workloads = [
            fw.Workload {name = "reliant-migrate", kind = "job", build = _go, args = ["db", "migrate", "up"], runtime = forge.OnHost {runner = "go-run"}}
            fw.Workload {name = "reliant-api-server", build = _go, args = ["server", "api"], runtime = forge.OnHost {}}
            fw.Workload {name = "reliant-temporal-worker", kind = "worker", build = _go, args = ["server", "worker"], runtime = forge.OnHost {}}
        ]
        frontends = [forge.Frontend {name = "web", type = "vite", path = "frontends/web", port = 5173, runtime = forge.OnHost {}}]
        infra = [forge.HostInfra {name = "postgres", port = 5432, database = "reliant", version = "16"}]
    }
}

output = forge.render(_bundle("auto"))
_local = forge.render(_bundle("local"))

_env = lambda o: any, name: str -> {str:str} {
    {e.name: e.value for e in ([w for w in o.workloads if w.name == name][0].spec.env or []) if e.value}
}

assert_renders_without_local_port = output.observability.local_port == 0 and output.observability.dev_telemetry == "auto"
assert_no_local_endpoint = output.observability.local_endpoint == None
assert_no_process_exports = all n in ["reliant-migrate", "reliant-api-server", "reliant-temporal-worker"] {"OTEL_EXPORTER_OTLP_ENDPOINT" not in _env(output, n)}
assert_service_identity_still_derived = _env(output, "reliant-api-server").OTEL_SERVICE_NAME == "reliant-api-server"
assert_declared_local_exports_nothing = _local.observability.local_endpoint == None and "OTEL_EXPORTER_OTLP_ENDPOINT" not in _env(_local, "reliant-api-server")
