"""
Negative test — two identities with the same name (the instance key).
"""

# expect: Bundle.identities: a name is declared twice
# expect: [users]

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identities = [forge.ManagedIdentity {name = "users"}, forge.ManagedIdentity {name = "users"}]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
