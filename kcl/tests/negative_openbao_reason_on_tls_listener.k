"""
Negative test — a transitional reason on a TLS listener is refused.
"""

# expect: sets transitional_reason but tls is on

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        listeners = [forge.OpenBaoListener {transitional_reason = "x"}]
    }

})
