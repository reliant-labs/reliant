"""
Negative test — an evaluation interval under 1m is refused.
"""

# expect: below the 1m floor

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", interval = "30s"}]
        }
    }
})
