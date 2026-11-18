"""
Negative test — a node_spread_reason on the default (required) spread excuses
nothing and is refused.
"""

# expect: node_spread is required

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        ha = forge.OpenBaoHA {node_spread_reason = "no reason needed"}
    }
})
