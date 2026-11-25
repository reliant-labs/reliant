"""
Negative test — a ShellBuild with a `source` runs in a checkout forge resolves
per invocation (the shared source cache, or a local override), so a profile
written under it by default would land nowhere lasting: every binary its
`pgo` names must say where with `out`.
"""

# expect: ShellBuild.pgo needs `out` on every binary when the build has a `source`
# expect: ./cmd/api

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.ShellBuild {
                source = forge.GitSource {repo = "github.com/acme/api", ref = "v1.2.3"}
                cmd = "make image"
                pgo = {"./cmd/api" = forge.ShellPGO {}}
            }
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
