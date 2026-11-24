"""
Negative test — an unknown pprof type is refused.
"""

# expect: PprofScrape.types must be a non-empty subset of

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
            profiles = [forge.PprofScrape {port = 6060, types = ["threadcreate"]}]
        }]
    }
})
