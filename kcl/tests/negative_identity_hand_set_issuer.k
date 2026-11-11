"""
Negative test — a workload that also hand-sets jwt_issuer.
"""

# expect: by hand AND Bundle.identity fills it
# expect: JWT_ISSUER (config field jwt_issuer)

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {}
    cluster_target = _prod
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", env = {JWT_ISSUER = "https://acme.auth0.com/"}, runtime = forge.OnCluster {target = _prod}}]
})
