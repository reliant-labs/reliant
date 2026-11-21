"""
Negative test — Frontend.apis names a workload this env does not declare.

The session cookie reaches every named API, so render admits only a
workload of this env's Bundle, and the refusal names the ones it has.
"""

# expect: Frontend 'site'.apis names a workload this environment does not declare
# expect: billing -> billing-api
# expect: declared: api

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        apis = {billing = "billing-api"}
    }]
})
