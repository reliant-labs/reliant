"""
Negative test — a ControlPlaneSecret on a cluster no connected cluster covers.

The control plane writes the Secret through a connected-cluster credential. A
cluster it holds none for is a Secret that is declared, recorded and never
delivered, so it is refused at render with the exact fix.
"""

# expect: control_plane_secrets [reliant-otel-auth]
# expect: names no connected cluster
# expect: forge cluster connect

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    control_plane_secrets = [forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
