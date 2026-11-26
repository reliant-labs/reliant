"""
Negative test — a token-shaped receiver is refused.
"""

# expect: looks like a URL or a credential

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            routes = [
                forge.AlertRoute {match = {}, receiver = "xoxb-123-456"}
            ]
        }
    }
})
