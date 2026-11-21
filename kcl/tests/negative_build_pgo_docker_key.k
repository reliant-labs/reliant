"""
Negative test — DockerBuild.pgo is keyed by a binary's package path
("./cmd/app"), the same spelling GoBuild.cmd uses, not by a binary name.
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
            build = forge.DockerBuild {pgo = {"app" = forge.PGO {enabled = False}}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
