"""
Negative test — go_memory_limit_percent is 0 (off) or 1..95.

GOMEMLIMIT is a fraction of the container memory limit. At or above the
limit the collector never gets ahead of the kernel's OOM killer, because the
limit also has to hold memory the Go runtime does not count.
"""

# expect: go_memory_limit_percent must be 0 (off) or 1..95

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo"}
            go_memory_limit_percent = 100
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
