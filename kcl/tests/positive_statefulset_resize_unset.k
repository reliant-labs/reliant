"""
Positive test — an env that declares no statefulset_resize projects no such key,
so every env that works today renders as before and the Go side applies its
default (a digest-pinned image, the Job enabled).
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

assert_key_absent = "statefulset_resize" not in output
