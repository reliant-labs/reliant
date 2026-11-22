"""
Negative test — a CPU capture longer than 30s is refused.
"""

# expect: PprofScrape.cpu_seconds must be 1-30

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
            profiles = [forge.PprofScrape {port = 6060, cpu_seconds = 60}]
        }]
    }
})
