"""
Negative test — alertname is set by the evaluator from `alert`.
"""

# expect: reserved

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", labels = {alertname = "x"}}]
        }
    }
})
