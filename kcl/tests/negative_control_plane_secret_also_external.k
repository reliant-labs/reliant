"""
Negative test — one Secret declared both as a ControlPlaneSecret and as an
ExternalSecret. One Secret has one provider; the second declaration would turn
a first deploy back into one the preflight blocks.
"""

# expect: also declared as a required_secrets ExternalSecret
# expect: One Secret has one provider

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    control_plane_secrets = [forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}]
    required_secrets = [forge.ExternalSecret {name = "reliant-otel-auth", namespace = "acme-prod", keys = ["token"]}]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
