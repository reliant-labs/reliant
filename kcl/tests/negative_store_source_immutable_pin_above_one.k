"""
Negative test — an immutable name pinned above 1 is refused.
"""

# expect: is above 1 but the name is `immutable`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            pin = {"P" = 2}
            immutable = ["P"]
        }
    }]
})
