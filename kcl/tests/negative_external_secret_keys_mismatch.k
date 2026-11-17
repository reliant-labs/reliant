"""
Negative test — keys that differ from what the source produces are refused.
"""

# expect: do not match what its source produces

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password", "extra"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
        }
    }]
})
