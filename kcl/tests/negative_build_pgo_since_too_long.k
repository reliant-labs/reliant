"""
Negative test — PGO.since is at most 720h: forge keeps 30 days of profile
edges, so a longer window would promise data that does not exist.
"""

# expect: PGO.since '721h' is longer than 720h

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo", pgo = forge.PGO {since = "721h"}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
