"""
Negative test — an audit config with no device is refused.
"""

# expect: has no device

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        audit = forge.OpenBaoAudit {stdout = False, file = False}
    }

})
