"""
render contract — forge.Telemetry on a HOSTED env (forge.OnHosted): an
image-only Workload whose metrics, logs and OTLP the Reliant platform
collects. Its OtlpPush env names one fact (recorded by Ref, its Value empty:
the platform resolves it) and sets one literal. A second, forge-built
workload declares no telemetry and contributes no target.

The vendor facts here (metric names, env keys) are illustrative fixtures, as a
project would declare them: forge knows none of them.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    workloads = [
        fw.Workload {
            name = "search"
            image = "docker.io/acme/search:1.4.0"
            ports = [fw.Port {name = "http", port = 9200}, fw.Port {name = "metrics", port = 9114}]
            telemetry = forge.Telemetry {
                metrics = [forge.MetricsScrape {
                    port = "metrics"
                    allow = "search_(queries_total|query_latency_seconds_(bucket|sum|count))"
                    drop_labels = ["shard"]
                }]
                otlp = forge.OtlpPush {
                    env = {
                        SEARCH_OTLP_ENDPOINT = forge.OtlpRef {key = "traces_endpoint"}
                        SEARCH_OTLP_ENABLED = "true"
                    }
                }
                logs = forge.LogParse {
                    format = "json"
                    severity = forge.LogSeverity {field = "level", mapping = {info = ["INFO"], warn = ["WARN"], error = ["ERROR"]}}
                    body = "message"
                }
            }
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/acme"
            build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHosted {}
        }
    ]
})
