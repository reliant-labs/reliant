"""
Negative test — a workload declares `config_secrets` but its env carries no
value for one of them (report d864fe2c). Nothing renders FROM config_secrets:
outside a forge Go project's config projection it delivered nothing, so a Node
app ran on its hard-coded JWT fallback. The render refuses, naming the
workload, the secret, and the line that delivers it.
"""

# expect: Workload.config_secrets names a credential its env never delivers
# expect: api: JWT_SECRET
# expect: forge.ManagedSecret

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "api"
            command = ["node", "dist/main.js"]
            config_secrets = ["JWT_SECRET"]
            env = {PORT = "3000"}
            runtime = forge.OnHost {listen_ports = []}
        }
    ]
})
