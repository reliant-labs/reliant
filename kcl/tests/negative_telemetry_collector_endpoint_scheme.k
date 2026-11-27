"""
Negative test — a collector's HTTP endpoint is a URL.
"""

# expect: TelemetryCollector.otlp_http_endpoint must be a URL with a scheme

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry_collector = forge.TelemetryCollector {namespace = "otel", pod_labels = {app = "otel"}, otlp_http_endpoint = "otel.otel.svc:4318"}}
})
