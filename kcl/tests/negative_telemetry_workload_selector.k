"""
Negative test — a Workload's telemetry targets the workload itself; a selector is refused.
"""

# expect: telemetry targets this workload itself

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {
        name = "sys", image = "docker.io/example/sys:1"
        ports = [fw.Port {name = "metrics", port = 9100}]
        telemetry = forge.Telemetry {selector = {app = "other"}, metrics = [forge.MetricsScrape {port = "metrics"}]}
        runtime = forge.OnCluster {target = _k3d}
    }]
})
