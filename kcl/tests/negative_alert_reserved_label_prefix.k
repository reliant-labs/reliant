"""
Negative test — a reliant_* label would let a rule impersonate the platform's own labels.
"""

# expect: reserved

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", labels = {reliant_org_id = "other"}}]
        }
    }
})
