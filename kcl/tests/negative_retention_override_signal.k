"""
Negative test — a RetentionOverride's signal is traces, metrics, logs or browser.
"""

# expect: RetentionOverride.signal

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        retention = forge.ObservabilityRetention {
            overrides = [forge.RetentionOverride {service = "worker", signal = "profiles", days = 1}]
        }
    }
})
