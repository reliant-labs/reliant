"""
Positive test — an AlertRule expr may open with SQL comments before its SELECT
or WITH, as pkg/alerting.ValidateExpr (the evaluator's check) accepts.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            rules = [
                forge.AlertRule {alert = "Line", expr = "-- why this rule exists\nSELECT 1 AS value"}
                forge.AlertRule {alert = "Hash", expr = "# owner: payments\n  select 1 as value"}
                forge.AlertRule {alert = "Block", expr = "/* multi\n   line */ WITH t AS (SELECT 1 AS value) SELECT value FROM t"}
                forge.AlertRule {alert = "Several", expr = "  -- one\n/* two */\n-- three\nSELECT 1 AS value"}
            ]
        }
    }
})

assert_four_rules = len(output.observability.alerts.rules) == 4
