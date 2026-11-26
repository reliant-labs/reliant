"""
Negative test — an OnExternal workload's image must name its registry: the
author's tooling pulls it, and forge pushes the build there.
"""

# expect: names no registry host
# expect: external runtime

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "api", build = forge.GoBuild {cmd = "./cmd/api"}, runtime = forge.OnExternal {}}]
})
