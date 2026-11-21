"""
Negative test — a funnel needs at least two steps.
"""

# expect: has 1 steps

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "f", steps = ["only"]}]}
    }]
})
