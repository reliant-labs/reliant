"""
Negative test — a capability outside the grantable set is refused.
"""

# expect: are not one of
# expect: 'sudo'

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "x", workload = "x", grants = [forge.OpenBaoGrant {path = "secret/data/a", capabilities = ["sudo"]}]}]
    }

})
