"""
Negative test — the HOST runtime refuses `enableServiceLinks`.

Service links are env vars Kubernetes injects into a pod for every Service in
its namespace. A local process is not a pod, so nothing would honour the
value. Refused, naming the workload and the field.
"""

# expect: api
# expect: enableServiceLinks

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo"}
        args = ["api"]
        enableServiceLinks = True
    }]]
})
