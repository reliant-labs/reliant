"""
Negative test — each goal is one dashboard row, keyed by its id.
"""

# expect: Analytics.goals declares a goal id twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            goals = [
                forge.Goal {id = "checkout", event = "purchase"}
                forge.Goal {id = "checkout", route = "/thanks"}
            ]
        }}
    }]
})
