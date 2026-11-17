"""
Negative test — `flux_delivery` configures the in-cluster Flux of an
`applier = "flux"` env; on any other env it would configure nothing.
"""

# expect: Bundle.flux_delivery configures the in-cluster Flux

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    flux_delivery = forge.FluxDelivery {}
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
