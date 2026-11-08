"""
Negative test — a generated name no data entry reads is refused.
"""

# expect: no `data` entry reads

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            generate = {"OTHER" = forge.GeneratedSecret {}}
        }
    }]
})
