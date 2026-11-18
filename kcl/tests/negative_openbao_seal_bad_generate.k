"""
Negative test — a static seal key that is not 32 hex bytes is refused.
"""

# expect: StaticSeal.key.generate must be

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        seal = forge.StaticSeal {key = forge.BootstrapSecret {name = "k", generate = forge.GeneratedSecret {bytes = 16}}}
    }

})
