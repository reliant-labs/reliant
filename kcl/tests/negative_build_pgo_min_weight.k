"""
Negative test — PGO.min_weight is a duration of CPU time and needs a unit.
"""

# expect: PGO.min_weight '10' is not a duration

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo", pgo = forge.PGO {min_weight = "10"}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
