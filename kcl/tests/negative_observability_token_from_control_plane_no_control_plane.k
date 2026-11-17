"""
Negative test — a control-plane destination token on an env with no control
plane to deliver it.
"""

# expect: need control_plane = forge.ControlPlane {}

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.reliantapi.com"
            token_from_control_plane = True
        }
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
