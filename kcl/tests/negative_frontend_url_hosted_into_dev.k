"""
Negative test — a dev server (forge.OnHost) may not take its SITE_URL from a
hosted site: only a build forge runs resolves a hosted hostname, and `forge
env up` builds nothing, so the variable would silently be empty.
"""

# expect: is a dev server (forge.OnHost), which forge does not build
# expect: Name a dev-served frontend of this env

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "mixed"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [
        forge.Frontend {name = "site", image = "site", path = "site", type = "vite", runtime = forge.OnHosted {}}
        forge.Frontend {
            name = "web"
            path = "."
            type = "vite"
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
            runtime = forge.OnHost {}
        }
    ]
})
