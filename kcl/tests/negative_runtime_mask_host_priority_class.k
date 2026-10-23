"""
Negative test — the HOST runtime refuses `priorityClassName`.

Pod priority ranks a pod against the others competing for a node. A local
process is not scheduled at all, so there is no pod to rank and nothing
would honour the value. Refused, naming the workload and the field.
"""

# expect: api
# expect: priorityClassName

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo"}
        args = ["api"]
        priorityClassName = "reliant-platform"
    }]]
})
