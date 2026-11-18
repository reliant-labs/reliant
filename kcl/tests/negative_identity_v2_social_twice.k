"""
Negative test — identity v2 V9 — two Google providers.
"""

# expect: at most one provider per type
# expect: [google] is declared twice

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        social = [forge.IdentityGoogle {client_id = "1.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}, forge.IdentityGoogle {client_id = "1.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
