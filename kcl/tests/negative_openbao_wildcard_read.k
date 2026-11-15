"""
Negative test — a glob read with no reason is refused, naming the escape.
"""

# expect: grants read on a glob
# expect: wildcard_read_reason

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "deployer", workload = "deployer", grants = [forge.OpenBaoGrant {path = "secret/data/envs/*", capabilities = ["read"]}]}]
    }

})
