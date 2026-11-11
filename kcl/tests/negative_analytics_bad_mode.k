"""
Negative test — Analytics.mode is one of the four tiers.
"""

# expect: Analytics.mode 'cookies' must be 'off' | 'aggregate' | 'identified' | 'full'

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {mode = "cookies"}}
    }]
})
