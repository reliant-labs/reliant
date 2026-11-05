"""
Negative test — identity v2 V5 — magic links on a hosted instance whose mail delivery is "none".
"""

# expect: offer "magic_link", but the identity can send no mail
# expect: drop email.delivery = "none"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        methods = ["password", "magic_link"]
        email = forge.IdentityEmail {delivery = "none"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
