"""
Negative test — source with out_of_band_reason is refused.
"""

# expect: sets both source and out_of_band_reason

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        out_of_band_reason = "x"
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
        }
    }]
})
