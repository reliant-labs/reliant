"""
Negative test — a consumer workload declared twice is refused.
"""

# expect: a workload is declared twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        consumers = [forge.OpenBaoConsumer {name = "a", workload = "w", delegated = True}, forge.OpenBaoConsumer {name = "b", workload = "w", delegated = True}]
    }

})
