"""
Negative test — a channel rule with no condition would credit every visit.
"""

# expect: Channel 'Partners' sets no condition

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            channels = [forge.Channel {channel = "Partners"}]
        }}
    }]
})
