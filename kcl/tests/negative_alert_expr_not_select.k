"""
Negative test — an expr that is not a SELECT (a DDL statement) is refused at render.
"""

# expect: must be a single SELECT

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "DROP TABLE otel_logs"}]
        }
    }
})
