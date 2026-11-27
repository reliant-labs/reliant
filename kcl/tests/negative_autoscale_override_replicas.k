"""
Negative test — a Bundle.overrides patch to an autoscaled workload's
Deployment spec.replicas.

The HorizontalPodAutoscaler owns spec.replicas, so the patch would reset what
it scaled to on every apply. Refused at load, naming the key; the range is
changed in `autoscale`. A patch to another field of the same Deployment is
fine (the fixture's second key), and so is the qualified key form.
"""

# expect: Deployment/acme-dev/api
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
        "Deployment/acme-dev/api" = {spec.replicas = 10}
        "Service/api" = {spec.sessionAffinity = "ClientIP"}
    }
})
