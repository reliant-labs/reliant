"""
Negative test — a DockerBuild.pgo key is a binary's package path in its one
spelling: a `.` or `..` segment ("./cmd/./app", "./cmd/../app") would name a
binary the build compares under another key, so the render and the build
could disagree about which binary it disables.
"""

# expect: DockerBuild.pgo is keyed by a binary's package path

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.DockerBuild {pgo = {"./cmd/../app" = forge.PGO {enabled = False}}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
