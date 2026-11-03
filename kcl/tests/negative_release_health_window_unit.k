"""
Negative test — release_health refuses: window = "15 minutes"
"""

# expect: use a number and s, m or h

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    release_health = forge.ReleaseHealth {window = "15 minutes"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
