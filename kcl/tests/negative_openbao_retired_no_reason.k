"""
Negative test — a retired auth mount with no reason is refused: disabling a
mount revokes every token it issued, so the declaration says what proves it unused.
"""

# expect: states no reason
# expect: revokes every token it issued

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}
    openbao = forge.OpenBao {
        retired_auth_mounts = [forge.OpenBaoRetiredAuthMount {name = "approle", reason = "  "}]
    }
})
