"""
Negative test — a Telemetry that declares no signal, no dashboard and no alert is refused.
"""

# expect: declares nothing to collect, draw or alert on

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
            isolated = True
        }]
    }
})
