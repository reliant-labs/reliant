"""
Negative test — ShellPGO.out is relative to the project root, like ShellBuild.cwd
reaching a sibling checkout with ../: an absolute path names one machine's
disk, so the declaration would write somewhere else on every other checkout.
"""

# expect: ShellPGO.out '/src/reliant/cmd/reliant/default.pgo' is relative to the project root

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.ShellBuild {
                cwd = "../reliant"
                cmd = "make"
                pgo = {"./cmd/reliant" = forge.ShellPGO {out = "/src/reliant/cmd/reliant/default.pgo"}}
            }
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
