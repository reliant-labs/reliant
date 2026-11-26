"""
Negative test — a Bundle.overrides patch to an autoscaled workload's
HorizontalPodAutoscaler.

forge renders the HPA from the declared range and behaviour; a patch would be
a second, invisible declaration of it. The range is changed in `autoscale`.
"""

# expect: HorizontalPodAutoscaler/api
# expect: patch the replica count of an autoscaled workload

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {
    cluster = "k3d-acme"
    namespace = "acme-dev"
}

output = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _k3d
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        autoscale = forge.Autoscale {minReplicas = 2, maxReplicas = 6}
        runtime = forge.OnCluster {target = _k3d}
    }]
    overrides = {
        "HorizontalPodAutoscaler/api" = {spec.maxReplicas = 20}
    }
})
