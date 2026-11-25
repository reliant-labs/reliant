"""
Negative test — telemetry with observability disabled is refused.
"""

# expect: Observability.telemetry is declared with enabled = False

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {enabled = False, telemetry = [forge.Telemetry {service_name = "eso", address = "127.0.0.1", checks = [forge.HealthCheck {port = 80}]}]}
})
