"""
Negative test — retiring the jwt-<cluster> mount the component manages is
refused at render: converge would enable it and then disable it on every run.
"""

# expect: jwt-demo
# expect: the JWT mount this component manages

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo", connected_cluster = "demo"}
    openbao = forge.OpenBao {
        retired_auth_mounts = [forge.OpenBaoRetiredAuthMount {name = "jwt-demo", reason = "wrong"}]
    }
})
