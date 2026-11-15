"""
Negative test — alerts on a disabled Observability evaluate nothing; declaring them is a mistake worth refusing.
"""

# expect: Observability.alerts is declared with enabled = False

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        enabled = False
        alerts = forge.Alerts {rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value"}]}
    }
})
