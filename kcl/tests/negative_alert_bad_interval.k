"""
Negative test — an interval that is not a duration is refused.
"""

# expect: interval 'soon' is not a duration

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", interval = "soon"}]
        }
    }
})
