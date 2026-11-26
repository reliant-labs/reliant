"""
Negative test — a default tuning cannot set reserved labels either.
"""

# expect: reserved

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            defaults = {"error-rate": forge.AlertDefault {labels = {reliant_x = "1"}}}
        }
    }
})
