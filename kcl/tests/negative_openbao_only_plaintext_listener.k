"""
Negative test — a transitional plaintext listener with no TLS listener beside it is refused.
"""

# expect: no TLS listener

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        listeners = [forge.OpenBaoListener {tls = False, transitional_reason = "migrating"}]
    }

})
