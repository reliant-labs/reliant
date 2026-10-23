"""
Negative test — Prometheus's `d` unit is not a duration here: the evaluator is Go and time.ParseDuration cannot read it, so it is refused at render, not at the first evaluation.
"""

# expect: is not a duration

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {rules = [forge.AlertRule {alert = "A", expr = "SELECT 1 AS value", "for" = "1d"}]}
    }
})
