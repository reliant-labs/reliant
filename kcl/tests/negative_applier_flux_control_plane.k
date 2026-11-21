"""
Negative test — `applier = "flux"` beside a control plane. An env with a
control plane is delivered by its hub; a second Flux forge installs would be a
second applier over the same objects.
"""

# expect: an env with a control plane is delivered by its hub

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    applier = "flux"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
