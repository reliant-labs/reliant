"""
Negative test — one Secret declared store-backed twice is refused.
"""

# expect: declared twice

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {}
    required_secrets = [
        forge.ExternalSecret {name = "a", namespace = "app", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "K"}}}
        forge.ExternalSecret {name = "a", namespace = "app", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "K2"}}}
    ]
})
