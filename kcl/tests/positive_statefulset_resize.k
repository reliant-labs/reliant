"""
Positive test — Bundle.statefulset_resize tunes the pre-rollout StatefulSet
volume-resize Job (image, disabled). The key is projected only when declared, so
an env that says nothing renders as before; the Job itself is rendered by Go.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    lifecycle = "local"
    statefulset_resize = forge.StatefulSetResize {image = "mirror.example/k8s:1.34", release_owner = "resize-hooks"}
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})

assert_image_is_projected = output.statefulset_resize.image == "mirror.example/k8s:1.34"
assert_enabled_by_default = output.statefulset_resize.disabled == False
assert_owner = output.statefulset_resize.release_owner == "resize-hooks"
