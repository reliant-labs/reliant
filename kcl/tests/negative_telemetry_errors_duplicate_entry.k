"""
Negative test — an errors.ignore entry may appear once.
"""

# expect: lists an entry twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {errors = forge.FrontendErrors {ignore = ["AbortError", "AbortError"]}}
    }]
})
