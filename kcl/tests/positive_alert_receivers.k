"""
Positive test — local receivers and the local alerting ports are projected
for `forge env up`, which renders the local Alertmanager from them.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alertmanager_port = 9093
        alerts_port = 9095
        alerts = forge.Alerts {
            receivers = [
                forge.AlertReceiver {name = "hook", type = "webhook", url = "http://localhost:9999/alerts"}
                forge.AlertReceiver {name = "slack", type = "slack", secret = "SLACK_WEBHOOK_URL", channel = "#alerts"}
                forge.AlertReceiver {name = "autopilot", type = "autopilot", url = "http://localhost:8080/hooks/a", secret = "AUTOPILOT_TOKEN", severities = ["*"]}
            ]
            routes = [
                forge.AlertRoute {match = {severity = "critical"}, receiver = "hook", continue = True}
                forge.AlertRoute {match = {}, receiver = "reliant-inapp"}
            ]
        }
    }
})

_o = output.observability
_r = _o.alerts.receivers
assert_ports = _o.alertmanager_port == 9093 and _o.alerts_port == 9095
assert_receivers_in_order = [r.name for r in _r] == ["hook", "slack", "autopilot"]
assert_webhook = _r[0].type == "webhook" and _r[0].url == "http://localhost:9999/alerts" and _r[0].secret == None
assert_slack = _r[1].secret == "SLACK_WEBHOOK_URL" and _r[1].channel == "#alerts" and _r[1].url == None
assert_autopilot_filter = _r[2].severities == ["*"] and _r[2].always_alertnames == ["NewIssue", "IssueRegressed", "IssueSpike"]
assert_default_filter = _r[0].severities == ["critical", "error"]

# With a destination there is no local ClickStack to evaluate against, so no
# local alerting loop either.
_dest = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alertmanager_port = 9093
        alerts_port = 9095
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
    }
})
assert_no_loop_with_destination = _dest.observability.alertmanager_port == None and _dest.observability.alerts_port == None
