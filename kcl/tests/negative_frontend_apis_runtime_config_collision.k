"""
Negative test — runtime_config declares `apis`, the key config.js publishes
Frontend.apis under.
"""

# expect: runtime_config.apis collides with Frontend.apis

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "site", image = "ghcr.io/acme/site", path = "site", type = "vite", public_dir = "dist"
        runtime = forge.OnHosted {}
        runtime_config = {apis = "x"}
        apis = {main = "api"}
    }]
})
