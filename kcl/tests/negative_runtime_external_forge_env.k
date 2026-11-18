"""
Negative test — OnExternal.env may not redefine a FORGE_* variable forge
sets for the hook: forge's value wins, so the line could only be silently
ignored.
"""

# expect: OnExternal.env may not set
# expect: FORGE_IMAGE

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", runtime = forge.OnExternal {env = {FORGE_IMAGE = "x"}}}]
})
