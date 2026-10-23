"""
Negative test — a store-delivered Secret refreshing faster than the cluster's
admission policy allows.
"""

# expect: refresh '1m' must be a duration of at least 5m

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
        refresh = "1m"
        store = forge.HostedStore {org = "o", env_id = "e"}
    }]
})
