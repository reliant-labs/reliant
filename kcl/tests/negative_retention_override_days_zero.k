"""
Negative test — a RetentionOverride keeps a signal at least one day.
"""

# expect: RetentionOverride.days must be >= 1

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        retention = forge.ObservabilityRetention {
            overrides = [forge.RetentionOverride {service = "worker", signal = "logs", days = 0}]
        }
    }
})
