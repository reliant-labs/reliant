"""
Negative test — a store-delivered Secret whose HostedStore has no org / env_id:
there is no path to read and no role to log in with.
"""

# expect: forge.HostedStore with no `org` / `env_id`

import forge

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    control_plane_secrets = [forge.ControlPlaneSecret {
        name = "reliant-otel-auth"
        cluster = _prod
        delivery = "store"
        store = forge.HostedStore {}
    }]
})
