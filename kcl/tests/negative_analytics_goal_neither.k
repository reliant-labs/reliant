"""
Negative test — a goal that names neither an event nor a route counts nothing.
"""

# expect: Goal 'checkout' must set exactly one of `event`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            goals = [forge.Goal {id = "checkout"}]
        }}
    }]
})
