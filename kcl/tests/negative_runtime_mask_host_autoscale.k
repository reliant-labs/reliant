"""
Negative test — the HOST runtime refuses `autoscale`, as it refuses
`replicas` > 1.

A host workload is one local process that `forge env up` launches once;
nothing runs an autoscaler there, so the declared range would be a promise
nothing keeps.
"""

# expect: api
# expect: autoscale: a host workload is one local process

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo"}
        args = ["api"]
        autoscale = forge.Autoscale {minReplicas = 2, maxReplicas = 4}
        runtime = forge.OnHost {runner = "go-run"}
    }]
})
