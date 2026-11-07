"""
Negative test — identity v2 V1 — signup = "domain" with no signup_domains.
"""

# expect: needs signup_domains, and signup_domains need signup = "domain"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        signup = "domain"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
