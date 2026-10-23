"""
Negative test — a plaintext listener without a transitional reason is refused.
"""

# expect: has tls = False and no transitional_reason
# expect: transitional_reason

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        listeners = [forge.OpenBaoListener {tls = False}]
    }

})
