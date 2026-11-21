"""
Negative test — the activation window is at most 90 days: the dashboards read
that far past a range, and product events are kept 90 days by default.
"""

# expect: Analytics.activation_window '91d' is not a window of at most 90d

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {activation_window = "91d"}}
    }]
})
