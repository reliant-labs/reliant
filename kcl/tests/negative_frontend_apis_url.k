"""
Negative test — a Frontend.apis value is a URL.

A named API is in the site's trust domain only when it is a workload this
env runs; an arbitrary URL would receive the site's session cookie.
"""

# expect: Frontend 'site'.apis values name a workload of this environment
# expect: never a URL

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        apis = {billing = "https://billing.example.com"}
    }]
})
