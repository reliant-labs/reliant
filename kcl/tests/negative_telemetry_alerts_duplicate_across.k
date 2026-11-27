"""
Negative test — a Telemetry alert shares the env's alert namespace.
"""

# expect: Telemetry alerts [SysDown] are declared twice

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {rules = [forge.AlertRule {alert = "SysDown", expr = "SELECT 1 AS value"}]}
    }
    workloads = [fw.Workload {
        name = "sys", image = "docker.io/example/sys:1"
        telemetry = forge.Telemetry {alerts = [forge.AlertRule {alert = "SysDown", expr = "SELECT 2 AS value"}]}
        runtime = forge.OnCluster {target = _k3d}
    }]
})
