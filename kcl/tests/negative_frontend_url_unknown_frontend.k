"""
Negative test — a forge.FrontendURL naming a frontend the environment does
not declare is refused at render, with the declared names listed. A dangling
reference would build a site whose canonical origin is silently empty.
"""

# expect: forge.FrontendURL names frontend 'site'

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "."
        type = "vite"
        env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
        runtime = forge.OnHost {}
    }]
})
