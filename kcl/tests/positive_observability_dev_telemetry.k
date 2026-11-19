"""
Positive test — Observability.dev_telemetry defaults to "auto" and renders
what the env declares; the dev store's retention (browser_days, overrides)
renders beside the existing hints.
"""

import forge

_defaults = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318}
})
_declared = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        dev_telemetry = "local"
        retention = forge.ObservabilityRetention {
            traces_days = 3
            browser_days = 2
            overrides = [forge.RetentionOverride {service = "worker", signal = "logs", days = 1}]
        }
    }
})
output = _declared

assert_default_is_auto = _defaults.observability.dev_telemetry == "auto"
assert_defaults_empty = _defaults.observability.retention.browser_days == 0 and len(_defaults.observability.retention.overrides) == 0
assert_declared_mode = _declared.observability.dev_telemetry == "local"
assert_declared_retention = _declared.observability.retention.browser_days == 2 and _declared.observability.retention.overrides[0].service == "worker" and _declared.observability.retention.overrides[0].signal == "logs" and _declared.observability.retention.overrides[0].days == 1
