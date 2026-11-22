"""
Negative test — a funnel window is a number and a unit of s, m, h or d.
"""

# expect: is not a window

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "f", steps = ["a", "b"], window = "2 weeks"}]}
    }]
})
