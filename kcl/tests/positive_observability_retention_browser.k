"""
Positive test — the browser-telemetry retention days are always rendered
(defaulted by the schema: replay 14, errors 30, product events 90), alongside
the backend-default hints, and carry what the env declares.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_ws = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_defaults = forge.render(forge.Bundle {
    project = "demo", workloads = _ws
    observability = forge.Observability {destination = _dest}
})
_declared = forge.render(forge.Bundle {
    project = "demo", workloads = _ws
    observability = forge.Observability {
        destination = _dest
        retention = forge.ObservabilityRetention {replay_days = 7, errors_days = 90, product_events_days = 180, logs_days = 5}
    }
})
output = _declared

assert_defaults_are_stated = _defaults.observability.retention.replay_days == 14 and _defaults.observability.retention.errors_days == 30 and _defaults.observability.retention.product_events_days == 90
assert_backend_hints_stay_zero = _defaults.observability.retention.logs_days == 0
assert_declared_days_render = _declared.observability.retention.replay_days == 7 and _declared.observability.retention.errors_days == 90 and _declared.observability.retention.product_events_days == 180
assert_old_hints_unchanged = _declared.observability.retention.logs_days == 5
