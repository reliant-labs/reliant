"""
Negative test — identity v2 V10 — a provider with no scopes.
"""

# expect: IdentityGitHub.scopes must not be empty

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        social = [forge.IdentityGitHub {client_id = "Iv1.abc", client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}, scopes = []}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
