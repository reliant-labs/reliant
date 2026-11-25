"""
Negative test — a service_name that is not a name is refused.
"""

# expect: Telemetry.service_name 'Sys Name' must match

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [forge.Telemetry {service_name = "Sys Name", address = "127.0.0.1", checks = [forge.HealthCheck {port = 80}]}]
    }
})
