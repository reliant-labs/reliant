"""
Negative test — a workload that points oidc_client_id at its own Secret.
"""

# expect: by hand AND Bundle.identity fills it

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {}
    cluster_target = _prod
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", env = {OIDC_CLIENT_ID = forge.SecretRef {name = "mine", key = "id"}}, runtime = forge.OnCluster {target = _prod}}]
})
