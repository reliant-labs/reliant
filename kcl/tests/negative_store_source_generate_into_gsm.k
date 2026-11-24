"""
Negative test — generation into Google Secret Manager is refused, naming BootstrapSecret as the escape.
"""

# expect: refused for a GCPSecretManagerStore
# expect: BootstrapSecret

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.GCPSecretManagerStore {project = "acme-prod"}
            data = {"password" = "p"}
            generate = {"p" = forge.GeneratedSecret {}}
        }
    }]
})
