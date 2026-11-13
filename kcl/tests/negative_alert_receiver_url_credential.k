"""
Negative test — a receiver URL carrying a credential is refused: the token
belongs in the secret store, named by `secret`.
"""

# expect: url carries a credential

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            receivers = [
                forge.AlertReceiver {name = "hook", type = "webhook", url = "https://hooks.example.com/x?token=abc"}
            ]
        }
    }
})
