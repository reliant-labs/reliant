"""
Negative test — a component naming something that cannot be a check name.
"""

# expect: must be uptime check names

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
    status_page = forge.StatusPage {
        title = "Acme status"
        components = [forge.StatusComponent {name = "Website", checks = ["Web Site"]}]
    }
}
output = forge.render(_b)
