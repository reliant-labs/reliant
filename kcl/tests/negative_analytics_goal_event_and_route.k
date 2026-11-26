"""
Negative test — a goal names an event OR a route, never both.
"""

# expect: Goal 'checkout' must set exactly one of `event`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            goals = [forge.Goal {id = "checkout", event = "purchase", route = "/checkout"}]
        }}
    }]
})
