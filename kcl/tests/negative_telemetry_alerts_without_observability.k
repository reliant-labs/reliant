"""
Negative test — a Telemetry's alerts need the env's observability.
"""

# expect: but the env's observability is unset or enabled = False

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {
        name = "sys", image = "docker.io/example/sys:1"
        telemetry = forge.Telemetry {alerts = [forge.AlertRule {alert = "SysDown", expr = "SELECT 1 AS value"}]}
        runtime = forge.OnCluster {target = _k3d}
    }]
})
