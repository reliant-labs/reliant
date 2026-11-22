"""
Negative test — `applier = "flux"` beside a declared `forge.flux_chart()`.
applier = "flux" installs Flux itself; two installs in one cluster fight over
the same CRDs.
"""

# expect: also declares the Flux chart

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "staging"
    cluster_target = _k3d
    applier = "flux"
    helm_charts = [forge.flux_chart()]
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
