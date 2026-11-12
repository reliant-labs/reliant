"""
Negative test — an autopilot receiver without `secret` is refused: every
delivery would be refused without the automation's bearer token.
"""

# expect: needs `secret`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            receivers = [
                forge.AlertReceiver {name = "autopilot", type = "autopilot", url = "http://localhost:8080/hooks/a"}
            ]
        }
    }
})
