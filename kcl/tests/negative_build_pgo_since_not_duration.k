"""
Negative test — PGO.since must be a Go duration in whole h/m/s: "7d" reads
naturally but Go's time.ParseDuration has no day unit.
"""

# expect: PGO.since '7d' is not a duration

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo", pgo = forge.PGO {since = "7d"}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
