"""
Negative test — `registry` is NOT a field on forge.ControlPlane either. A
hosted environment has no registry for the same reason a cluster one has none:
each workload declares its own, as part of its image.

ControlPlane is a closed schema, so the leftover field is a load error, not a
line that quietly does nothing.
"""

# expect: registry

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "app"
    control_plane = forge.ControlPlane {
        endpoint = "https://cp.example.com"
        registry = "ghcr.io/acme"
    }
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnHosted {}
    }]
})
