"""
Negative test — `reliant-inapp` cannot be declared: it is always there
(locally, the alert log).
"""

# expect: 'reliant-inapp' is reserved

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            receivers = [
                forge.AlertReceiver {name = "reliant-inapp", type = "webhook", url = "http://localhost:9999/x"}
            ]
        }
    }
})
