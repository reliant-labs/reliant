"""
Negative test — a target that is not hosted.
"""

# expect: does not run on the hosted runtime

import forge
import forge.workloads as fw

_b = forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }, fw.Workload {
        name = "worker"
        image = "ghcr.io/acme/worker:v1"
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-x", namespace = "x"}}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    uptime = forge.Uptime {
        checks = [forge.UptimeCheck {name = "x", target = "worker"}]
    }
}
output = forge.render(_b)
