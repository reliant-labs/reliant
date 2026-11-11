"""
Negative test — hub_converges beside applier = "forge". applier says the hub
converges no unit; hub_converges says it converges these. Once the first unit
moves, the applier declaration is dropped.
"""

# expect: sets both applier = "forge" and hub_converges
# expect: Drop applier

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    applier = "forge"
    hub_converges = ["base"]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
