"""
Negative test — a route goal is a path, matched against page_viewed's route,
which never carries a query string.
"""

# expect: Goal 'pricing'.route '/pricing?plan=pro' is not a route

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            goals = [forge.Goal {id = "pricing", route = "/pricing?plan=pro"}]
        }}
    }]
})
