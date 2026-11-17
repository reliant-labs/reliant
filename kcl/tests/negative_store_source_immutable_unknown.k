"""
Negative test — an immutable name the source does not read is refused.
"""

# expect: StoreSource.immutable
# expect: does not read

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            immutable = ["Q"]
        }
    }]
})
