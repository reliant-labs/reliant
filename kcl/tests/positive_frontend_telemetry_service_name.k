"""
Positive test — `telemetry.service_name` declares the service.name a frontend
built from ANOTHER repository bakes (that repository's project and frontend
name, e.g. "shop-web"), which is neither this project's name nor the name this
environment deploys it under. It renders under `telemetry.service_name`; an
in-repo frontend declares none and renders none.
"""

import forge

output = forge.render(forge.Bundle {
    project = "platform"
    frontends = [
        forge.Frontend {
            name = "store", type = "vite", port = 5173
            source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0", subdir = "web"}
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {service_name = "shop-web"}
        }
        forge.Frontend {
            name = "admin", path = "admin", type = "vite", port = 5174
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {}
        }
    ]
})

_by = {f.name: f for f in output.frontends}
assert_declared_name_renders = _by.store.telemetry.service_name == "shop-web"
assert_in_repo_declares_none = _by.admin.telemetry.service_name == Undefined or _by.admin.telemetry.service_name == None
