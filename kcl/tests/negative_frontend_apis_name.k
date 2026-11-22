"""
Negative test — a Frontend.apis name is not a path segment the route can
carry (uppercase here).
"""

# expect: Frontend 'site'.apis names must match

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        apis = {Billing = "api"}
    }]
})
