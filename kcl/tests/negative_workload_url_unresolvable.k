"""
Negative test — a reference to a workload with no URL forge can know at
render time is REFUSED rather than guessed. Here the host workload declares
no listen_ports: `forge env up` hands it an ephemeral port only after
rendering, so any URL written now would be wrong.
"""

# expect: forge.WorkloadURL 'api' has no URL forge can know at render time

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "fixture"
    workloads = [_w | {runtime = forge.OnHost {}} if not _w.runtime else _w for _w in [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/api"}, args = ["server"]}]]
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        runtime = forge.OnHost {}
        runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
    }]
}

output = forge.render(_bundle)
