"""
Negative test — an IdentityRef in an env that declares no identity.
"""

# expect: needs Bundle.identity

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", env = {X = forge.IdentityRef {output = "issuer"}}, runtime = forge.OnCluster {target = _prod}}]
})
