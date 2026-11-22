"""
Negative test — an even Raft replica count is refused.
"""

# expect: Raft needs an odd number

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        replicas = 4
    }

})
