"""
Negative test — a target that names nothing in the Bundle.
"""

# expect: is not a workload or frontend of this Bundle

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {
        checks = [forge.UptimeCheck {name = "x", target = "nobody"}]
    }
})
