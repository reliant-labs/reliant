"""
Negative test — applier = "forge" beside lifecycle. They answer one question
opposite ways: lifecycle says the env's clusters are scratch that forge creates
and deletes; applier says the clusters are REAL and forge is deliberately the
one that applies them. An env cannot be both.
"""

# expect: sets both applier = "forge" and lifecycle = "local"
# expect: opposite ways

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    applier = "forge"
    lifecycle = "local"
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
