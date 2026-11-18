"""
Negative test — a pin below 1 is refused.
"""

# expect: StoreSource.pin
# expect: version of 1 or more

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            pin = {"P" = 0}
        }
    }]
})
