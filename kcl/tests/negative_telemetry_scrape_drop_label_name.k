"""
Negative test — drop_labels must be label names.
"""

# expect: MetricsScrape.drop_labels must be Prometheus label names

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
            metrics = [forge.MetricsScrape {port = 9100, drop_labels = ["user-id"]}]
        }]
    }
})
