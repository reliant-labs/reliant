"""
Negative test — a store on a Secret the control plane pushes: the store would
be ignored, and the author would believe it was used.
"""

# expect: sets `store` but delivers by push

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
        store = forge.HostedStore {org = "o", env_id = "e"}
    }]
})
