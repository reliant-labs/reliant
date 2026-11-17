"""
Negative test — a refresh that is not a duration is refused.
"""

# expect: StoreSource.refresh
# expect: at least 5m

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            refresh = "often"
        }
    }]
})
