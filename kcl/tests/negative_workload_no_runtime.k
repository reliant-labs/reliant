"""
Negative test — a workload with no runtime.

Every workload in an env states where it runs (ADR 0002 §2): there is no
env-level default, so an env is a list of per-workload bindings. The
old rule was "a workload without a host block needs a cluster_target" —
an env-wide guess about where it went. Now there is nothing to guess: an
unbound workload is refused, by name.

Replaces negative_workloads_without_cluster_target.k and
negative_self_hosted_simple_backend_no_cluster.k.
"""

# expect: orphan
# expect: runtime

import forge
import forge.workloads as fw

# A bound sibling does not lend its runtime to the unbound one.
output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [
        fw.Workload {name = "bound", image = "docker.io/library/nginx:1", runtime = forge.OnHost {runner = "binary"}, command = ["nginx"]}
        fw.Workload {name = "orphan", image = "orphan-img"}
    ]
})
