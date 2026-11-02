"""
Negative test — delivery = "store" names no store: the control plane would not
know which store to write.
"""

# expect: ControlPlaneSecret 'reliant-otel-auth' has delivery = "store" and no `store`

import forge

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    control_plane_secrets = [forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod, delivery = "store"}]
})
