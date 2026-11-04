"""
Negative test — public on a Gateway the Bundle does not declare is refused.
"""

# expect: is not a forge.Gateway in Bundle.gateways

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "edge"}
    }

})
