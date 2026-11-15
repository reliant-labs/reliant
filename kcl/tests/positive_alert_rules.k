"""
Positive test — forge.AlertRule / AlertRoute / AlertDefault on Observability.alerts.

Rules render with severity defaulted to warning, the Prometheus `for` / interval
defaults, a user severity kept, and `defaults` tuning passed through for Go to
resolve. Routes are rendered as declared; with none, Go supplies the in-app +
Slack fan-out (internal/cli/env_alerts.go), never a pager.
"""

import forge

_rules = forge.Alerts {
    rules = [
        forge.AlertRule {alert = "Plain", expr = "SELECT 'x' AS service, 1 AS value"}
        forge.AlertRule {
            alert = "Tuned"
            expr = "WITH t AS (SELECT 1 AS value) SELECT value FROM t"
            "for" = "1h30m"
            interval = "5m"
            keep_firing_for = "10m"
            labels = {severity = "critical", team = "payments"}
            annotations = {summary = "value is {{ $value }}"}
        }
        forge.AlertRule {alert = "Off", expr = "SELECT 1 AS value", enabled = False}
    ]
    defaults = {"error-rate": forge.AlertDefault {enabled = False}, "p95-latency": forge.AlertDefault {labels = {severity = "critical"}, "for" = "15m", params = {threshold_seconds = 2.5}}}
}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, alerts = _rules}
})

_a = output.observability.alerts
_by = {r.alert: r for r in _a.rules}

assert_three_rules = len(_a.rules) == 3
assert_for_defaults_to_5m = _by.Plain["for"] == "5m"
assert_interval_defaults_to_1m = _by.Plain.interval == "1m"
assert_severity_defaults_to_warning = _by.Plain.labels.severity == "warning"
assert_user_severity_kept = _by.Tuned.labels.severity == "critical" and _by.Tuned.labels.team == "payments"
assert_compound_duration = _by.Tuned["for"] == "1h30m" and _by.Tuned.interval == "5m" and _by.Tuned.keep_firing_for == "10m"
assert_annotations_kept = _by.Tuned.annotations.summary == "value is {{ $value }}"
assert_disabled_rule_still_rendered = _by.Off.enabled == False
assert_default_tuning_passed_through = _a.defaults["error-rate"].enabled == False and _a.defaults["p95-latency"].params.threshold_seconds == 2.5 and _a.defaults["p95-latency"]["for"] == "15m"
assert_no_routes_declared_means_none_rendered = len(_a.routes) == 0
