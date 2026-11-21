"""
Negative test — two scrapes of one system need a name each but one.
"""

# expect: several scrapes need a `name` each but one

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
            metrics = [forge.MetricsScrape {port = 9100}, forge.MetricsScrape {port = 9101}]
        }]
    }
})
