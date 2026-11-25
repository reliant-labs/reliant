"""
Negative test — a leading SQL comment does not make a non-SELECT expr a SELECT.
"""

# expect: must be a single SELECT

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "/* select */ -- select\nDROP TABLE otel_logs"}]
        }
    }
})
