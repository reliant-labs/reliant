"""
Negative test — identity v2 V7 — an owner listed twice, differing only in case.
"""

# expect: an address is listed twice (compared case-insensitively)

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["Boss@acme.test", "boss@acme.test"]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
