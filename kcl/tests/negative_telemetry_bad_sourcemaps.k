"""
Negative test — sourcemaps is publish | public | off.
"""

# expect: sourcemaps must be 'publish' | 'public' | 'off'

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {sourcemaps = "inline"}
    }]
})
