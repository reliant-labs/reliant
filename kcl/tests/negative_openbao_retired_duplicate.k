"""
Negative test — a retired auth mount listed twice is refused.
"""

# expect: a mount is listed twice
# expect: approle

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}
    openbao = forge.OpenBao {
        retired_auth_mounts = [
            forge.OpenBaoRetiredAuthMount {name = "approle", reason = "unused"}
            forge.OpenBaoRetiredAuthMount {name = "approle", reason = "unused"}
        ]
    }
})
