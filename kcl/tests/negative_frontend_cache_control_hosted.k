"""
Negative test — cache_control on a frontend the platform serves.

The platform owns the bucket, the CDN and the headers: a cache rule on an
OnHosted frontend has no carrier and is refused rather than dropped. (A
`cdn` cannot be written at all: OnHosted has no such field — see
closedschema_frontend_hosted_bucket.k.)
"""

# expect: cache_control
# expect: hosted

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "web"
        type = "vite"
        cache_control = [forge.CacheRule {pattern = "**", cache_control = "no-cache"}]
        runtime = forge.OnHosted {}
    }]
})
