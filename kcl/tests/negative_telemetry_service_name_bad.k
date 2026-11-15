"""
Negative test — telemetry.service_name is a service name, not free text.
"""

# expect: BrowserTelemetry.service_name must be a service name

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", type = "vite", port = 5173
        source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0"}
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {service_name = "shop web!"}
    }]
})
