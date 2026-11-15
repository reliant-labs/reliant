"""
Negative test — an invalid store name is refused.
"""

# expect: are not valid openbao store names

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "bad-name"}
        }
    }]
})
