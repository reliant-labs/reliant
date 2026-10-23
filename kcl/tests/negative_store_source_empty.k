"""
Negative test — a source producing no Secret key is refused.
"""

# expect: StoreSource produces no Secret key

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
        }
    }]
})
