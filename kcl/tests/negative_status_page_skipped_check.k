"""
Negative test — a component naming a derived check the env skips: a skipped check
is not in the final list, so it has no data to show.
"""

# expect: Admin: site

import forge
import forge.workloads as fw

_b = forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {skip = ["site"]}
    status_page = forge.StatusPage {
        title = "Acme status"
        components = [forge.StatusComponent {name = "Admin", checks = ["site"]}]
    }
}
output = forge.render(_b)
