"""
Negative test — a funnel is counted per session or per user.
"""

# expect: by must be 'session' | 'user'

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "f", steps = ["a", "b"], by = "device"}]}
    }]
})
