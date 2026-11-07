"""
Negative test — a ControlPlaneSecret on an env that declares no control plane.
Nothing would mint, deliver or renew it.
"""

# expect: need control_plane = forge.ControlPlane {}

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane_secrets = [forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
