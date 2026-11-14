"""
Positive test — a sidecar declared `native = True` with a startup probe reaches
the Workload record's spec verbatim, so the renderer can emit it as an
initContainer with restartPolicy: Always (the Cloud SQL proxy ordering fix).
A sidecar that does not say native stays absent of the field.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5050/demo"
        args = ["api"]
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = _k3d}
        sidecars = [
            fw.Container {
                name = "proxy"
                image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
                native = True
                ports = [fw.Port {name = "proxy-health", port = 9801}]
                probes = fw.Probes {startupPath = "/startup", readinessPath = "/readiness", livenessPath = "/liveness"}
            }
            fw.Container {name = "plain", image = "gcr.io/p/plain:1"}
        ]
    }]
})
_sc = [w for w in output.workloads if w.name == "api"][0].spec.sidecars
assert_native_kept = _sc[0].native == True
assert_startup_path_kept = _sc[0].probes.startupPath == "/startup"
assert_plain_not_native = "native" not in _sc[1] or _sc[1].native != True
