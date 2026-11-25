"""
Negative test — a port outside 1-65535 is refused.
"""

# expect: MetricsScrape.port must be 1-65535 or an IANA port name

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
            metrics = [forge.MetricsScrape {port = 70000}]
        }]
    }
})
