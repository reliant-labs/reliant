"""
Negative test — an OpenBaoStore with no Bundle.openbao is refused, with both escapes.
"""

# expect: read from a forge.OpenBaoStore
# expect: declares no `openbao`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
        }
    }]
})
