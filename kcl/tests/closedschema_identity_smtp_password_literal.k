"""
Closed-schema test — an SMTP password given as a literal instead of a ManagedSecret name.
"""

# expect: expected ManagedSecret

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {smtp = forge.IdentitySMTP {host = "h", sender = "a@b.test", password = "hunter2"}}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
