"""
Negative test — two generated kinds whose companions collide are refused.
"""

# expect: companions
# expect: collide

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["a", "b"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"a" = "A", "b" = "B"}
            generate = {"A" = forge.GeneratedSecret {kind = "rsa_2048", companion = "PUB"}, "B" = forge.GeneratedSecret {kind = "rsa_2048", companion = "PUB"}}
        }
    }]
})
