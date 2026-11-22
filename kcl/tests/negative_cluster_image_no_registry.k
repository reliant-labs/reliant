"""
Negative test — a workload bound to a CLUSTER runtime must declare an image
that names its own registry HOST, because the registry is declared on the
image and nowhere else. There is no env-level registry to fall back on.

A bare `api` used to be completed by an env-level field. With no env declaring
a registry, a bare name renders as `api:<tag>`, which the kubelet resolves
against Docker Hub and fails to pull — an ImagePullBackOff minutes after a
deploy reported success. So it fails at KCL LOAD, naming the workload.
"""

# expect: names no registry host

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_proj_us-central1_prod"
    namespace = "app-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "app"
    workloads = [fw.Workload {
        name = "api"
        image = "api"
        runtime = forge.OnCluster {target = _prod}
    }]
})
