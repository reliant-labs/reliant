"""
Positive test — hub_converges = [] is a DECLARATION, not an absence: the hub
converges no unit, which is the same statement as applier = "forge". The key is
projected (as an empty list) so forge ends its inference instead of resuming it.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    hub_converges = []
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})

assert_empty_list_is_projected = output.hub_converges == []
