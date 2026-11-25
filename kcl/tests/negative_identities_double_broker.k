"""
Negative test — two identities left to derive their applications both broker the same workload.
"""

# expect: workload 'api' is the native_broker of identities [identity, users]
# expect: declare `applications` on each

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identities = [forge.ManagedIdentity {}, forge.ManagedIdentity {name = "users"}]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
