"""
Negative test — a receiver is a NAME; a webhook URL there would commit a secret.
"""

# expect: never a URL or a key

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            routes = [
                forge.AlertRoute {match = {}, receiver = "https://hooks.slack.com/services/T0/B0/xyz"}
            ]
        }
    }
})
