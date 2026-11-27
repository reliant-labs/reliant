"""
Negative test — a profile pull more often than every minute is refused.
"""

# expect: PprofScrape.interval must be a duration of at least 1m

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
            profiles = [forge.PprofScrape {port = 6060, interval = "30s"}]
        }]
    }
})
