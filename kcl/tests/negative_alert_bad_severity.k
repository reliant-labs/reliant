"""
Negative test — severity is critical | warning | info.
"""

# expect: severity must be one of

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", labels = {severity = "page-me"}}]
        }
    }
})
