"""
Negative test — the COMPOSE runtime refuses `autoscale`, as it refuses
`replicas`.

The compose file owns a compose service's scale, and compose runs no
autoscaler, so the range would be silently ignored.
"""

# expect: indexer
# expect: autoscale: the compose file owns the scale

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "indexer"
        kind = "worker"
        autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
        runtime = forge.OnCompose {}
    }]
})
