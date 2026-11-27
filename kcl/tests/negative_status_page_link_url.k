"""
Negative test — a link that is not an http(s) address.
"""

# expect: must be an absolute http(s) address

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
        links = [forge.StatusLink {label = "Help", url = "javascript:alert(1)"}]
        components = [forge.StatusComponent {name = "Website", checks = ["site"]}]
    }
}
output = forge.render(_b)
