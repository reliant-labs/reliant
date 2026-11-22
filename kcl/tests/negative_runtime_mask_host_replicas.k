"""
Negative test — the HOST runtime refuses `replicas` > 1.

A host workload is one local process. `forge env up` launches it once;
three replicas would be a number nothing honours, and the author would
believe they were testing concurrency they were not. Refused, naming the
workload and the field. (storageGiB, RBAC, CRDs and SA annotations are
refused on host for the same reason — see the runtime profile table.)
"""

# expect: api
# expect: replicas

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/demo"}
        args = ["api"]
        replicas = 3
    }]]
})
