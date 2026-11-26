"""
Negative test — an identity name the issuer slug cannot carry.
"""

# expect: Bundle.identities names [9users] must match ^[a-z][a-z0-9-]{0,23}$

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identities = [forge.ManagedIdentity {}, forge.ManagedIdentity {name = "9users"}]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
