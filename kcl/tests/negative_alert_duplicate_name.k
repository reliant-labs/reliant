"""
Negative test — two rules with one name are ambiguous in routing and history.
"""

# expect: twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [
                forge.AlertRule {alert = "Dup", expr = "SELECT 1 AS value"}
                forge.AlertRule {alert = "Dup", expr = "SELECT 2 AS value"}
            ]
        }
    }
})
