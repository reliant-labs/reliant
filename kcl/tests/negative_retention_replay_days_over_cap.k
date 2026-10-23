"""
Negative test — session replay is retained at most 30 days.
"""

# expect: replay_days must be 1..30

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        retention = forge.ObservabilityRetention {replay_days = 31}
    }
})
