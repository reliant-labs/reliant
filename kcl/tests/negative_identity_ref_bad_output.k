"""
Negative test — an IdentityRef naming an output that does not exist.
"""

# expect: IdentityRef.output 'password' must be one of

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", env = {X = forge.IdentityRef {output = "password"}}, runtime = forge.OnCluster {target = _prod}}]
})
