"""
Negative test — Workload.resource_metrics is one of the modes
forge/pkg/observe knows (auto, all, process, host, off). Anything else would
render a FORGE_OBSERVE_RESOURCE_METRICS the runtime ignores with a warning.
"""

# expect: resource_metrics must be one of

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo"}
            resource_metrics = "everything"
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
