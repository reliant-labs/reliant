"""
Positive test — OTLP facts (forge.OtlpRef, forge.otlp_value) resolve per
runtime, and a Workload's Telemetry remaps them into the system's own env
keys:

  * host: the local collector on 127.0.0.1:local_port; no gRPC fact (forge
    does not know the local gRPC port), so a key bound to it is left out
  * cluster: the env's telemetry_collector when declared, else the node
    agent (rendered with a destination)
  * hosted: literals only; every endpoint fact is the platform's, so the
    target records the ref with no value
  * observability off: no endpoint key at all

and Telemetry.service_name / scope reach the standard OTEL_* env.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

_tel = fw.Telemetry {
    service_name = "sys-svc"
    otlp = fw.OtlpPush {
        env = {
            SYS_EXPORTER = "otlp"
            SYS_ENDPOINT = forge.OtlpRef {key = "endpoint"}
            SYS_TRACES = forge.OtlpRef {key = "traces_endpoint"}
            SYS_GRPC = forge.OtlpRef {key = "grpc_address"}
            SYS_INSECURE = forge.OtlpRef {key = "insecure"}
            SYS_NAME = forge.OtlpRef {key = "service_name"}
        }
    }
}

_sys = fw.Workload {name = "sys", image = "docker.io/example/sys:1", command = ["/usr/bin/sys"], telemetry = _tel}

_env = lambda out: any, name: str -> {str:} {
    {e.name: e.value for e in [w for w in out.workloads if w.name == name][0].spec.env}
}
_target = lambda out: any, name: str -> any {
    [t for t in out.telemetry if t.source == "workload:" + name][0]
}

_host = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318}
    workloads = [_sys | {runtime = forge.OnHost {}}]
})
_he = _env(_host, "sys")
assert_host_endpoint = _he.SYS_ENDPOINT == "http://127.0.0.1:4318"
assert_host_signal_endpoint = _he.SYS_TRACES == "http://127.0.0.1:4318/v1/traces"
assert_host_no_grpc = "SYS_GRPC" not in _he
assert_host_insecure = _he.SYS_INSECURE == "true"
assert_host_literal = _he.SYS_EXPORTER == "otlp"
assert_service_name_fact = _he.SYS_NAME == "sys-svc"
assert_service_name_standard = _he.OTEL_SERVICE_NAME == "sys-svc"
assert_project_scope_identity = _he.OTEL_RESOURCE_ATTRIBUTES == "deployment.environment.name=dev,service.namespace=demo"
assert_host_target_ref = _target(_host, "sys").otlp.env.SYS_GRPC == {"ref": "grpc_address", "value": ""}

_cluster = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry_collector = forge.TelemetryCollector {
            namespace = "otel"
            pod_labels = {app = "otel"}
            otlp_http_endpoint = "https://otel.otel.svc:4318/"
            otlp_grpc_address = "otel.otel.svc:4317"
        }
    }
    workloads = [_sys | {runtime = forge.OnCluster {target = _k3d}}]
})
_ce = _env(_cluster, "sys")
assert_cluster_collector_endpoint = _ce.SYS_ENDPOINT == "https://otel.otel.svc:4318/"
assert_cluster_collector_traces = _ce.SYS_TRACES == "https://otel.otel.svc:4318/v1/traces"
assert_cluster_collector_grpc = _ce.SYS_GRPC == "otel.otel.svc:4317"
assert_cluster_tls = _ce.SYS_INSECURE == "false"
assert_cluster_target = _target(_cluster, "sys").cluster == "k3d-demo" and _target(_cluster, "sys").namespaces == ["demo-dev"] \
    and _target(_cluster, "sys").selector == {"app.kubernetes.io/name": "sys"}

_agent = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
    }
    workloads = [_sys | {
        runtime = forge.OnCluster {target = _k3d}
        telemetry = _tel | {otlp = forge.OtlpPush {protocol = "grpc", env = {SYS_ENDPOINT = forge.OtlpRef {key = "endpoint"}, SYS_GRPC = forge.OtlpRef {key = "grpc_address"}}}}
    }]
})
_ae = _env(_agent, "sys")
assert_agent_grpc_endpoint = _ae.SYS_ENDPOINT == "http://otel-agent.forge-observability.svc.cluster.local:4317"
assert_agent_grpc_address = _ae.SYS_GRPC == "otel-agent.forge-observability.svc.cluster.local:4317"

# Workload declarations take precedence over environment defaults, while
# literal workload env still wins when it sets a sampler or transport.
_sdk = fw.Telemetry {otlp = fw.OtlpPush {protocol = "grpc", sampler_ratio = 0.25, env = forge.otlp_env()}}
_override = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        sampling = forge.ObservabilitySampling {traces_ratio = 0.1}
        telemetry_collector = forge.TelemetryCollector {
            namespace = "otel"
            pod_labels = {app = "otel"}
            otlp_http_endpoint = "https://otel.otel.svc:4318/"
            otlp_grpc_address = "otel.otel.svc:4317"
        }
    }
    workloads = [
        fw.Workload {name = "defaults", image = "docker.io/example/sys:1", runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "sdk", image = "docker.io/example/sys:1", telemetry = _sdk, runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {
            name = "literal", image = "docker.io/example/sys:1", telemetry = _sdk, runtime = forge.OnCluster {target = _k3d}
            env = {OTEL_TRACES_SAMPLER = "always_off", OTEL_EXPORTER_OTLP_ENDPOINT = "https://sdk.example.com", OTEL_EXPORTER_OTLP_PROTOCOL = "http/protobuf"}
        }
    ]
})
_de = _env(_override, "defaults")
_se = _env(_override, "sdk")
_le = _env(_override, "literal")
assert_default_sampler = _de.OTEL_TRACES_SAMPLER_ARG == "0.1"
assert_workload_sampler = _se.OTEL_TRACES_SAMPLER == "parentbased_traceidratio" and _se.OTEL_TRACES_SAMPLER_ARG == "0.25"
assert_workload_transport = _se.OTEL_EXPORTER_OTLP_PROTOCOL == "grpc" and _se.OTEL_EXPORTER_OTLP_ENDPOINT == "http://otel.otel.svc:4317"
assert_literal_sampler = _le.OTEL_TRACES_SAMPLER == "always_off" and "OTEL_TRACES_SAMPLER_ARG" not in _le
assert_literal_transport = _le.OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf" and _le.OTEL_EXPORTER_OTLP_ENDPOINT == "https://sdk.example.com"

_hosted = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {}
    workloads = [_sys | {runtime = forge.OnHosted {}}]
})
_hoe = _env(_hosted, "sys")
assert_hosted_literal_only = _hoe.SYS_EXPORTER == "otlp" and "SYS_ENDPOINT" not in _hoe and "SYS_GRPC" not in _hoe
assert_hosted_ref_recorded = _target(_hosted, "sys").otlp.env.SYS_ENDPOINT == {"ref": "endpoint", "value": ""}
assert_hosted_target = _target(_hosted, "sys").runtime == "hosted" and _target(_hosted, "sys").selector == {"app.kubernetes.io/name": "sys"}

_off = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [_sys | {runtime = forge.OnHost {}, telemetry = _tel | {scope = "platform"}}]
})
_oe = _env(_off, "sys")
assert_off_no_endpoint = "SYS_ENDPOINT" not in _oe and "SYS_TRACES" not in _oe and "SYS_INSECURE" not in _oe
assert_platform_scope_no_identity = "OTEL_RESOURCE_ATTRIBUTES" not in _oe

_obs = forge.Observability {local_port = 4318, destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
assert_otlp_value_host = forge.otlp_value("endpoint", _obs) == "http://127.0.0.1:4318"
assert_otlp_value_cluster = forge.otlp_value("logs_endpoint", _obs, "k3d-demo") == "http://otel-agent.forge-observability.svc.cluster.local:4318/v1/logs"
assert_otlp_value_cluster_grpc = forge.otlp_value("endpoint", _obs, "k3d-demo", "grpc") == "http://otel-agent.forge-observability.svc.cluster.local:4317"
assert_otlp_value_off = forge.otlp_value("endpoint", forge.Observability {enabled = False}) == ""
assert_otlp_env_keys = sorted([k for k in forge.otlp_env()]) == ["OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_RESOURCE_ATTRIBUTES", "OTEL_SERVICE_NAME"]
