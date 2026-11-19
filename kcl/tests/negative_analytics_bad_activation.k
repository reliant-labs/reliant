"""
Negative test — the activation is a product event name.
"""

# expect: Analytics.activation 'order created' is not a product event name

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {activation = "order created"}}
    }]
})
