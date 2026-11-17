"""
Negative test — relaxing the HA node spread to a preference without saying why
is refused: two replicas on one node lose quorum together when it fails.
"""

# expect: states no node_spread_reason

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        ha = forge.OpenBaoHA {node_spread = "preferred"}
    }
})
