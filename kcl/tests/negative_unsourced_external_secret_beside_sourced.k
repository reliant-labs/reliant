"""
Negative test — an unsourced ExternalSecret beside a sourced one is refused until it says why.
"""

# expect: declare no `source`
# expect: out_of_band_reason

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {}
    required_secrets = [
        forge.ExternalSecret {name = "a", namespace = "app", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "K"}}}
        forge.ExternalSecret {name = "b", namespace = "app", keys = ["k"]}
    ]
})
