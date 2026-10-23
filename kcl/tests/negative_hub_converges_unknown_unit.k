"""
Negative test — hub_converges names a unit nothing declares. A typo here would
hand the hub nothing while the author believed a unit had moved, so it is
refused with the units that do exist.
"""

# expect: names a unit that is not declared
# expect: infra

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    delivery_units = [forge.DeliveryUnit {name = "infra"}]
    hub_converges = ["infar"]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", delivery_unit = "infra", runtime = forge.OnCluster {target = _prod}}]
})
