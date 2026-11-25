"""
Negative test — a grant path that would inject HCL is refused.
"""

# expect: OpenBaoGrant.path
# expect: rendered into an HCL policy

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "x", workload = "x", grants = [forge.OpenBaoGrant {path = "secret/data/a\" { capabilities = [\"sudo\"] }", capabilities = ["read"]}]}]
    }

})
