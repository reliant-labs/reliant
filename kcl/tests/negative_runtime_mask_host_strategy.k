"""
Negative test — the HOST runtime refuses `strategy`.

A rollout strategy is how a Deployment replaces its pods. A host workload
is one local process that `forge env up` starts once, so there is no
Deployment and nothing would honour the value. Refused, naming the workload
and the field.
"""

# expect: api
# expect: strategy

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo"}
        args = ["api"]
        strategy = "Recreate"
    }]]
})
