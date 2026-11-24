"""
Negative test — Observability.telemetry has nothing to default service_name from.
"""

# expect: Observability.telemetry: each entry needs a service_name

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry = [forge.Telemetry {address = "127.0.0.1", checks = [forge.HealthCheck {port = 80}]}]}
})
