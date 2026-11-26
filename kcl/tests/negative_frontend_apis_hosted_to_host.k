"""
Negative test — an OnHosted frontend names, in Frontend.apis, a workload the
platform does not run. The platform routes /_api/@<name>, so it cannot reach
a host process or a cluster you operate.
"""

# expect: Frontend 'site'.apis.billing
# expect: does not run on the hosted runtime

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}
        fw.Workload {name = "billing-api", build = forge.GoBuild {cmd = "./cmd/billing"}, runtime = forge.OnHost {listen_ports = [8090]}}
    ]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        apis = {billing = "billing-api"}
    }]
})
