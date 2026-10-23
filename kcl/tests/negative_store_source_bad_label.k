"""
Negative test — StoreSource.labels are Kubernetes labels.
"""

# expect: StoreSource.labels

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "tok"
        namespace = "app"
        keys = ["token"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"token" = "TOK"}
            labels = {"has space" = "x"}
        }
    }]
})
