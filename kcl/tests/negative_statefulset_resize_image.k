"""
Negative test — a resize Job image with whitespace in it. An image reference is
one token; a typo'd "alpine/k8s 1.34" would otherwise fail at pull time on the
cluster, in the middle of a deploy, rather than at render.
"""

# expect: must be an image reference with no whitespace

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    lifecycle = "local"
    statefulset_resize = forge.StatefulSetResize {image = "alpine/k8s 1.34"}
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
