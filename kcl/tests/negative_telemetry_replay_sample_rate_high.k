"""
Negative test — replay_sample_rate is a share of sessions, 0 to 1.
"""

# expect: replay_sample_rate must be between 0.0 and 1.0

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {replay_sample_rate = 1.5}
    }]
})
