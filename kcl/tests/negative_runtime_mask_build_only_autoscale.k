"""
Negative test — the BUILD-ONLY runtime refuses `autoscale`, as it refuses
`replicas`: a build-only workload is never run, so there is nothing to scale.
"""

# expect: api
# expect: autoscale: a build-only workload is never run

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/api"}
        autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
        runtime = forge.BuildOnly {}
    }]
})
