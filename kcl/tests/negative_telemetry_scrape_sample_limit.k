"""
Negative test — a sample_limit above the ceiling is refused.
"""

# expect: MetricsScrape.sample_limit must be 1-200000

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [forge.Telemetry {
            service_name = "sys"
            cluster = "k3d-demo"
            selector = {app = "sys"}
            metrics = [forge.MetricsScrape {port = 9100, sample_limit = 500000}]
        }]
    }
})
