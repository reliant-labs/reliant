"""
Negative test — DockerBuild.coverage is keyed by a binary's package path
("./cmd/app"), the same spelling as DockerBuild.pgo, not by a binary name.
"""

# expect: DockerBuild.coverage is keyed by a binary's package path

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.DockerBuild {coverage = {"app" = True}}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
