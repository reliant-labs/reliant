"""
Negative test — a path on a tcp check is refused.
"""

# expect: HealthCheck.path and scheme are http-only

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
            checks = [forge.HealthCheck {port = 80, path = "/healthz"}]
        }]
    }
})
