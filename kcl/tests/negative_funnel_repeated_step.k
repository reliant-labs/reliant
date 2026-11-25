"""
Negative test — a funnel step may appear once.
"""

# expect: repeats a step

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "f", steps = ["a", "b", "a"]}]}
    }]
})
