"""
Negative test — a workload env var that names an undeclared workload with
forge.WorkloadURL is refused at load, on a HOSTED workload too (where it
would otherwise reach the control plane and wait forever on a reference
nothing satisfies).
"""

# expect: CORS_ORIGINS
# expect: website

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {CORS_ORIGINS = forge.WorkloadURL {workload = "website"}}
        runtime = forge.OnHosted {}
    }]
}

output = forge.render(_bundle)
