"""
Positive test — an env that declares no applier projects `applier: null`, a
stable key, so a consumer can tell "undeclared" from "forge applies it on
purpose". The distinction is the whole point of recording it.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    lifecycle = "local"
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})

assert_applier_is_null = output.applier == None
assert_lifecycle_unaffected = output.lifecycle == "local"
