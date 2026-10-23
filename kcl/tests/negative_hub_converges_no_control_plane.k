"""
Negative test — hub_converges without a control plane. The hub is the control
plane's, so there is nothing to do the converging.
"""

# expect: needs control_plane = forge.ControlPlane

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    hub_converges = ["base"]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
