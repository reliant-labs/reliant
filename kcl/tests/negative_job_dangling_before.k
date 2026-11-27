"""
Negative test — a Job whose `before` names a workload that does not exist in
the environment is rejected at LOAD time, by forge.render over the Bundle's
workloads (one schema cannot see its siblings).

Without this check the failure is silent and worse than a crash: the job
renders, nothing gates on it, and the ordering the author declared simply
does not happen. The dependent starts against an unprovisioned world and
fails somewhere else entirely, at runtime, in a message that names none of
this.
"""

# expect: provision
# expect: apiserver

import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnCluster {target = _t}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "localhost:5050/demo"}
        fw.Workload {
            kind = "job"
            name = "provision"
            image = "localhost:5050/demo"
            args = ["provision"]
            # `apiserver` does not exist — the workload is called `api`.
            before = ["apiserver"]
        }
    ]]
})
