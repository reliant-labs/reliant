"""
Negative test — a consumer with no grants that is not delegated is refused.
"""

# expect: has no grants and is not delegated

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "x", workload = "x"}]
    }

})
