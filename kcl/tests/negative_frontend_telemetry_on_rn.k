"""
Negative test — a react-native frontend runs no browser SDK, so it takes no
browser telemetry declaration.
"""

# expect: runs no browser SDK

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "mobile", path = "mobile", type = "rn", port = 8081
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {}
    }]
})
