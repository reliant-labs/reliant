"""
render contract — forge.Telemetry on all four attachments: an image-only
cluster Workload (named ports, a regex log parse, OtlpPush env remaps, an
alert), a HostInfra engine, a raw Manifests group, and an
Observability.telemetry entry for a system outside the bundle, collected by
the project's own collector (Observability.telemetry_collector). The raw
group's Telemetry declares a typed dashboard (forge.dashspec).

The vendor facts here (metric names, env keys, log layout) are illustrative
fixtures, as a project would declare them: forge knows none of them.
"""
# kcl-args: env="dev"

import forge
import forge.dashspec as ds
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry_collector = forge.TelemetryCollector {
            namespace = "platform-otel"
            pod_labels = {"app.kubernetes.io/name" = "otel-agent"}
            otlp_http_endpoint = "http://otel-agent.platform-otel.svc:4318"
            otlp_grpc_address = "otel-agent.platform-otel.svc:4317"
        }
        telemetry = [forge.Telemetry {
            service_name = "cert-manager"
            scope = "platform"
            cluster = "k3d-acme"
            namespaces = ["cert-manager"]
            selector = {"app.kubernetes.io/name" = "cert-manager"}
            metrics = [forge.MetricsScrape {port = 9402, allow = "certmanager_certificate_.*"}]
            logs = forge.LogParse {
                format = "json"
                severity = forge.LogSeverity {field = "v", mapping = {info = ["0", "1", "2"], debug = ["3", "4", "5"]}}
            }
        }]
    }
    workloads = [
        fw.Workload {
            name = "temporal"
            image = "docker.io/temporalio/server:1.26.2"
            ports = [fw.Port {name = "grpc", port = 7233}, fw.Port {name = "metrics", port = 9090}]
            telemetry = forge.Telemetry {
                metrics = [forge.MetricsScrape {
                    port = "metrics"
                    label_limit = 0
                    label_value_length_limit = 0
                    allow = "service_(requests|errors|latency_(bucket|sum|count))"
                    drop_labels = ["workflowType"]
                    counters = "service_requests"
                }]
                otlp = forge.OtlpPush {
                    protocol = "grpc"
                    sampler_ratio = 0.25
                    env = {
                        TEMPORAL_OTEL_EXPORTER = "otlp"
                        TEMPORAL_OTEL_ENDPOINT = forge.OtlpRef {key = "grpc_address"}
                        TEMPORAL_SERVICE = forge.OtlpRef {key = "service_name"}
                    }
                }
                logs = forge.LogParse {
                    format = "regex"
                    regex = r"^(?P<ts>\S+) (?P<lvl>[A-Z]+) (?P<msg>.*)$"
                    timestamp = forge.LogTimestamp {field = "ts"}
                    severity = forge.LogSeverity {field = "lvl", mapping = {info = ["INFO"], warn = ["WARN"], error = ["ERROR"]}}
                    body = "msg"
                    rules = [forge.LogSeverityRule {body_match = "^history size exceeds", from_severity = "error", severity = "warn"}]
                }
                profiles = [forge.PprofScrape {port = 7936, types = ["cpu", "heap", "goroutine"]}]
                checks = [forge.HealthCheck {port = "grpc"}]
                alerts = [forge.AlertRule {alert = "TemporalErrors", expr = "SELECT 1 AS value"}]
            }
            runtime = forge.OnCluster {target = _k3d}
        }
    ]
    infra = [forge.HostInfra {
        name = "nats"
        engine = "nats"
        port = 4222
        nats_monitor_port = 8222
        telemetry = forge.Telemetry {
            metrics = [forge.MetricsScrape {port = "monitor", path = "/varz", allow = "nats_.*"}]
        }
    }]
    manifests = [forge.Manifests {
        name = "clickhouse"
        namespace = "observability"
        cluster = "k3d-acme"
        telemetry = [forge.Telemetry {
            selector = {"app" = "clickhouse"}
            isolated = True
            # The project owns no policy for these pods: forge renders one.
            network_policy = True
            scope = "platform"
            metrics = [forge.MetricsScrape {
                port = 9363
                allow = "ClickHouseMetrics_.*"
                bearer_token = forge.SecretRef {name = "ch-metrics", key = "token"}
                scheme = "https"
                tls = forge.ScrapeTLS {ca = forge.SecretRef {name = "ch-ca", key = "ca.crt"}}
            }]
            checks = [forge.HealthCheck {kind = "http", port = 8123, path = "/ping"}]
            dashboards = [ds.Dashboard {
                id = "clickhouse-parts"
                title = "ClickHouse parts"
                panels = [ds.Panel {
                    id = "parts", title = "Active parts", kind = "line", signal = "metrics"
                    series = [ds.gauge("parts", "ClickHouseMetrics_PartsActive")]
                }]
            }]
        }]
    }]
})
