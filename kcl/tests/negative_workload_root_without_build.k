"""
Negative test — a workload's `root` names a root of a forge-built binary.

A root is a //forge:root struct the binary's own command tree runs, so a
workload with no build (a third-party image) runs none.
"""

# expect: names a root of a forge-built binary

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "nats"
            image = "docker.io/library/nats:2.10"
            root = "server"
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
