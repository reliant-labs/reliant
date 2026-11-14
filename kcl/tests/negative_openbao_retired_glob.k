"""
Negative test — a retired auth mount name is rendered into an ACL path, so a
glob or a path is refused: the converge policy never gains a wildcard.
"""

# expect: must be an auth mount name
# expect: no slash or glob

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}
    openbao = forge.OpenBao {
        retired_auth_mounts = [forge.OpenBaoRetiredAuthMount {name = "jwt-*", reason = "all of them"}]
    }
})
