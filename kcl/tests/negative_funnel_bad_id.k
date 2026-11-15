"""
Negative test — a funnel id becomes a panel id.
"""

# expect: is not a valid id

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "my funnel", steps = ["a", "b"]}]}
    }]
})
