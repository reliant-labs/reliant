"""
Negative test — an errors.ignore "re:" entry must compile.
"""

# expect: not a valid regular expression

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {errors = forge.FrontendErrors {ignore = ["re:(unclosed"]}}
    }]
})
