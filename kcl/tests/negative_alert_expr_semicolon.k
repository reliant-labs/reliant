"""
Negative test — a second statement smuggled after a SELECT is refused.
"""

# expect: single statement

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value; DROP TABLE otel_logs"}]
        }
    }
})
