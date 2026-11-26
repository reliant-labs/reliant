"""
Negative test — two telemetry tokens for one cluster and namespace. A workload
reads exactly one OTLP credential, so there would be no right one to mount.
"""

# expect: at most one telemetry_token

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    control_plane_secrets = [
        forge.ControlPlaneSecret {name = "otel-a", cluster = _prod}
        forge.ControlPlaneSecret {name = "otel-b", cluster = _prod}
    ]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
