"""
Negative test — an EnvVar carries exactly one source: a frontend_url beside a
literal value is ambiguous (which one does the build see?), so it is refused
where it is written.
"""

# expect: EnvVar 'SITE_URL' sets more than one source

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "."
        type = "vite"
        env_vars = [forge.EnvVar {name = "SITE_URL", value = "https://example.com", frontend_url = forge.FrontendURL {frontend = "web"}}]
        runtime = forge.OnHost {}
    }]
})
