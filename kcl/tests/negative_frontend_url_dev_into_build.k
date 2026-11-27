"""
Negative test — a published build may not take its origin from a dev server:
the release would carry http://localhost as the site's canonical origin.
"""

# expect: is a dev server (forge.OnHost)
# expect: the build would carry http://localhost

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [
        forge.Frontend {name = "preview", path = "preview", type = "vite", runtime = forge.OnHost {}}
        forge.Frontend {
            name = "web"
            image = "web"
            path = "."
            type = "vite"
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "preview"}}]
            runtime = forge.OnHosted {}
        }
    ]
})
