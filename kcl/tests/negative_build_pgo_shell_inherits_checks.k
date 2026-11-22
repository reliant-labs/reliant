"""
Negative test — forge.ShellPGO is a PGO, so every PGO check holds for it too:
a window longer than the profile edges are kept reads nothing more.
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
            build = forge.ShellBuild {cwd = "../reliant", cmd = "make", pgo = {"./cmd/reliant" = forge.ShellPGO {since = "721h"}}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
