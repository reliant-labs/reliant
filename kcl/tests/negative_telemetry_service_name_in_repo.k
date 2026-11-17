"""
Negative test — an in-repo frontend reports <project>-<name>; telemetry.service_name
is for a frontend built from another repository.
"""

# expect: telemetry.service_name is for a frontend built from another repository

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {service_name = "other-web"}
    }]
})
