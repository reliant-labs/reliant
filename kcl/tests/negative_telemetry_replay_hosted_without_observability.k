"""
Negative test — a hosted frontend that switches session replay on in an env
that declares no observability: the platform keeps replay events only for an
env whose observability policy opts in, and this env has no policy to carry
the opt-in, so every recording would be dropped.
"""

# expect: telemetry.replay = True
# expect: declares no `observability`

import forge

output = forge.render(forge.Bundle {
    project = "demo", env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [forge.Frontend {
        name = "web", image = "ghcr.io/acme/web", path = "web", type = "vite"
        runtime = forge.OnHosted {}
        telemetry = forge.BrowserTelemetry {replay = True}
    }]
})
