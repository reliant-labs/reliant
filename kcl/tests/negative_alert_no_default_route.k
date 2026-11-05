"""
Negative test — routes with no empty-match route leave some alert unrouted.
"""

# expect: no default route

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {
            routes = [forge.AlertRoute {match = {severity = "critical"}, receiver = "oncall"}]
        }
    }
})
