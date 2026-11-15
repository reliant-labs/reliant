"""
Negative test — a basic-auth Secret needs username and password keys.
"""

# expect: kubernetes.io/basic-auth

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db-login"
        namespace = "app"
        keys = ["password"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "DB_PASSWORD"}
            secret_type = "kubernetes.io/basic-auth"
        }
    }]
})
