"""
Negative test — a Secret key with two producers is refused.
"""

# expect: in both `data` and `templates`

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            templates = {"password" = "x"}
        }
    }]
})
