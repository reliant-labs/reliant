"""
Negative test — a HostedStore with no control plane is refused.
"""

# expect: forge.HostedStore
# expect: control_plane = forge.ControlPlane {}

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password"]
        source = forge.StoreSource {
            store = forge.HostedStore {}
            data = {"password" = "P"}
        }
    }]
})
