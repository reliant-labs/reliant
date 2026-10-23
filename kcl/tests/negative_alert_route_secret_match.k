"""
Negative test — a match value shaped like a URL or key is refused.
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
                forge.AlertRoute {match = {team = "https://hooks.slack.com/x"}, receiver = "slack"}
                forge.AlertRoute {match = {}, receiver = "slack"}
            ]
        }
    }
})
