"""
Negative test — generating the recovery escrow is refused.
"""

# expect: is WRITTEN by the converge Job

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        recovery = forge.OpenBaoRecovery {escrow = forge.BootstrapSecret {name = "r", generate = forge.GeneratedSecret {}}}
    }

})
