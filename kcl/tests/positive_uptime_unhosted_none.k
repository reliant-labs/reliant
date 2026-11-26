"""
Positive test — an env that is not hosted derives no uptime checks: a local
env (lifecycle) and an env with no control plane have nothing outside to probe.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
})

assert_no_checks = "uptime_checks" not in output
