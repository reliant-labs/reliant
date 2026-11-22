"""
Negative test — StaticSiteCDN.invalidate rejects a value outside the enum.

Invalidation is rate-limited and each request is a global purge; a typo'd
"entrypoint" must fail at render, not quietly stop invalidating HTML.
"""

# expect: StaticSiteCDN.invalidate must be 'entrypoints'

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        path = "web"
        runtime = forge.OnBucket {bucket = "b", cdn = forge.StaticSiteCDN {url_map = "lb", invalidate = "entrypoint"}}
    }]
})
