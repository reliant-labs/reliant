"""
Negative test — `applier = "flux"` on an env that spans two clusters. The Flux
forge installs applies only in the cluster it runs in.
"""

# expect: which needs exactly ONE cluster

import forge
import forge.workloads as fw

_a = forge.ClusterTarget {cluster = "gke_acme_a", namespace = "acme", platform = "amd64"}
_b = forge.ClusterTarget {cluster = "gke_acme_b", namespace = "acme", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _a
    applier = "flux"
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _a}}
        fw.Workload {name = "worker", image = "ghcr.io/acme/worker", runtime = forge.OnCluster {target = _b}}
    ]
})
