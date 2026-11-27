"""
Negative test — a cron's concurrencyPolicy is Kubernetes' enum, spelled as
Kubernetes spells it. Anything else fails the generated tier check, naming the
field and the values it takes.
"""

# expect: Workload.concurrencyPolicy must be one of
# expect: "Forbid"

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {
        name = "nightly"
        kind = "cron"
        image = "localhost:5050/demo"
        args = ["sweep"]
        schedule = "@daily"
        concurrencyPolicy = "forbid"
        runtime = forge.OnCluster {target = _k3d}
    }]
})
