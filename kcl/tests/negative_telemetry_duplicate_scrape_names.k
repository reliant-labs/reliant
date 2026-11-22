"""
Negative test — scrape names are unique within a Telemetry.
"""

# expect: Telemetry.metrics names must be unique

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
            metrics = [forge.MetricsScrape {name = "a", port = 9100}, forge.MetricsScrape {name = "a", port = 9101}]
        }]
    }
})
