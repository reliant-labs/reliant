"""
Negative test — an OnExternal hook that is an EMPTY argv is refused. "No
hook" is spelled by leaving the field out; an empty list would otherwise
read as a hook that runs nothing and succeeds.
"""

# expect: OnExternal.deploy is an empty argv

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", runtime = forge.OnExternal {deploy = []}}]
})
