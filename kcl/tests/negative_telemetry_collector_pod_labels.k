"""
Negative test — a collector names its pods.
"""

# expect: TelemetryCollector.pod_labels must name at least one non-empty label

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry_collector = forge.TelemetryCollector {namespace = "otel", pod_labels = {}}}
})
