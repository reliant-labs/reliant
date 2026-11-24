"""
Negative test — Bundle.identity and Bundle.identities are both set.
"""

# expect: Bundle.identity and Bundle.identities are both set

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {}
    identities = [forge.ManagedIdentity {name = "users"}]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
