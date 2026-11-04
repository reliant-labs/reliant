"""
Negative test — Bundle.project is required and non-empty. It is every
rendered object's app.kubernetes.io/part-of, the handle a CD pipeline
selects one release's workloads by; a guessed default (each object's own
name, or the first workload's) silently selects the wrong set.
"""

# expect: Bundle.project is required

import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "k3d-x", namespace = "x"}
_rt = forge.OnCluster {target = _t}

output = forge.render(forge.Bundle {
    project = ""
    cluster_target = _t
    workloads = [
        fw.Workload {name = "first", image = "docker.io/library/busybox:1", runtime = _rt}
        fw.Workload {name = "second", image = "docker.io/library/busybox:1", runtime = _rt}
    ]
})
