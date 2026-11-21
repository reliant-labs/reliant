"""
Negative test — two funnels of one frontend cannot share an id.
"""

# expect: declares a funnel id twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [
            forge.Funnel {id = "f", steps = ["a", "b"]}
            forge.Funnel {id = "f", steps = ["c", "d"]}
        ]}
    }]
})
