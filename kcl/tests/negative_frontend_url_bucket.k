"""
Negative test — a forge.FrontendURL to a frontend on your own bucket is
refused: forge does not know the hostname your CDN answers on, and a guessed
origin is a canonical link that hands the site's ranking to another host.
"""

# expect: whose public hostname forge does not know
# expect: Set SITE_URL to the site's URL as a literal

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "."
        type = "vite"
        env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "web"}}]
        runtime = forge.OnBucket {bucket = "gs://acme-site"}
    }]
})
