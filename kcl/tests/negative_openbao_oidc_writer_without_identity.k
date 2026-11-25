"""
Negative test — an OIDC writer with no ManagedIdentity is refused, naming the default as the escape.
"""

# expect: OpenBaoOIDCWriter
# expect: declares no `identity`
# expect: OpenBaoSAWriter

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        writer = forge.OpenBaoOIDCWriter {role = "admin"}
    }

})
