"""
Negative test — hub_converges beside lifecycle = "local". A local env's clusters
are scratch forge applies directly; there is no hub to hand a unit to.
"""

# expect: sets both hub_converges
# expect: there is no hub to hand a unit to

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    lifecycle = "local"
    control_plane = forge.ControlPlane {}
    hub_converges = ["base"]
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
