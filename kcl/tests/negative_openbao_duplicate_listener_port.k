"""
Negative test — a repeated listener port is refused.
"""

# expect: declare a port twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        listeners = [forge.OpenBaoListener {}, forge.OpenBaoListener {}]
    }

})
