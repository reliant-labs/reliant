"""
Negative test — the local Alertmanager and the local evaluator deliver to
each other, so one port without the other is refused.
"""

# expect: alertmanager_port and alerts_port go together

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alertmanager_port = 9093
    }
})
