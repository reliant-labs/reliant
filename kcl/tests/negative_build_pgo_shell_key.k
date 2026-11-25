"""
Negative test — ShellBuild.pgo is keyed by a binary's package path relative to
cwd ("./cmd/reliant"), the spelling DockerBuild.pgo uses, not by a binary
name: `go build` reads <package>/default.pgo, so the key must be a package.
"""

# expect: ShellBuild.pgo is keyed by a binary's package path relative to cwd

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.ShellBuild {cwd = "../reliant", cmd = "make", pgo = {"reliant" = forge.ShellPGO {}}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
