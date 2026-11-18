"""
Positive test — an env that declares no hub_converges projects no such key, so
forge keeps its own inference and every env that works today renders as before.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    lifecycle = "local"
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})

assert_key_absent = "hub_converges" not in output
