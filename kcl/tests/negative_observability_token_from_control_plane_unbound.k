"""
Negative test — a control-plane destination on a cluster that is not connected:
the derived ControlPlaneSecret has no credential to be delivered with.
"""

# expect: names no connected cluster
# expect: forge-otlp-token

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.reliantapi.com"
            token_from_control_plane = True
        }
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
