"""
Negative test — a HOSTED frontend's runtime_config names a HOST workload.

The control plane resolves a hosted frontend's references against what IT
runs; a process on the author's machine is unreachable from it. Refused at
render, naming both ends, exactly as for a hosted workload.
"""

# expect: site
# expect: api
# expect: does not run on the hosted runtime

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/api"}, args = ["api"], runtime = forge.OnHost {listen_ports = [8085]}}]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
        runtime = forge.OnHosted {}
    }]
})
