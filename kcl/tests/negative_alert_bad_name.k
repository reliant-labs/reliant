"""
Negative test — an alert name is a metric-style identifier.
"""

# expect: must be a metric-style name

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "not a name", expr = "SELECT 1 AS value"}]
        }
    }
})
