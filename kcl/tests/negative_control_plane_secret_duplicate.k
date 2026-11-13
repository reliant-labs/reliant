"""
Negative test — the same ControlPlaneSecret declared twice for one cluster and
namespace: the control plane would write one Secret from two declarations.
"""

# expect: declared twice for the same cluster and namespace

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    control_plane_secrets = [
        forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}
        forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}
    ]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
