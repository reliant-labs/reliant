"""
Negative test — the cookieless visitor id changes every UTC day, so a funnel
counted by visitor cannot span more than a day.
"""

# expect: Funnel 'buy' is counted by visitor
# expect: must be at most 1d

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {id = "buy", steps = ["a", "b"], by = "visitor", window = "2d"}]}
    }]
})
