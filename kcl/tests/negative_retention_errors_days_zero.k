"""
Negative test — errors_days is at least 1.
"""

# expect: errors_days must be 1..365

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        retention = forge.ObservabilityRetention {errors_days = 0}
    }
})
