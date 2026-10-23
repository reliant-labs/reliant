"""
Negative test — a pin on a name the source does not read is refused.
"""

# expect: StoreSource.pin
# expect: must name a store name this source reads

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            pin = {"OTHER" = 1}
        }
    }]
})
