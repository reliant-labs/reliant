"""
Negative test — a frontend runtime_config forge.WorkloadURL naming a workload
the environment does not declare is refused at LOAD time, with the declared
names listed. A dangling reference would otherwise ship a config.js whose
API_URL is empty, and the app would call its own origin.
"""

# expect: forge.WorkloadURL names workload 'apiserver'

import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "fixture"
    workloads = [_w | {runtime = forge.OnHost {listen_ports = [8085]}} if not _w.runtime else _w for _w in [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/api"}, args = ["server"]}]]
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        runtime = forge.OnHost {}
        runtime_config = {API_URL = forge.WorkloadURL {workload = "apiserver"}}
    }]
}

output = forge.render(_bundle)
