"""
Negative test — a writer service account that is also a consumer is refused.
"""

# expect: is also a consumer's workload

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "x", workload = "forge-secret-writer", delegated = True}]
    }

})
