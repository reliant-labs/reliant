"""
Positive test — declared routes replace the in-app + Slack default; a
default (empty-match) route must be present, receivers are names.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            routes = [
                forge.AlertRoute {match = {severity = "critical"}, receiver = "pagerduty-oncall", continue = True}
                forge.AlertRoute {match = {team = "payments"}, receiver = "slack-payments"}
                forge.AlertRoute {match = {}, receiver = "slack"}
            ]
        }
    }
})

_r = output.observability.alerts.routes
assert_routes_in_order = [r.receiver for r in _r] == ["pagerduty-oncall", "slack-payments", "slack"]
assert_continue_kept = _r[0]["continue"] == True and _r[1]["continue"] == False
assert_match_kept = _r[0].match.severity == "critical"
assert_default_route_present = any r in _r { len(r.match) == 0 }
