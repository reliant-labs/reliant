"""
Negative test — two checks with one name.
"""

# expect: names a check twice

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
    uptime = forge.Uptime {
        checks = [forge.UptimeCheck {name = "x", url = "https://example.com/"}, forge.UptimeCheck {name = "x", url = "https://example.org/"}]
    }
}
output = forge.render(_b)
