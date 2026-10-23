"""
Negative test — BYO TLS without a CA ConfigMap is refused.
"""

# expect: needs ca_configmap

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        tls = forge.OpenBaoTLS {secret = "my-tls"}
    }

})
