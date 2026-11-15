"""
Negative test — requiring a gate by a name the ledger records its own verdict
under.
"""

# expect: the ledger records its own verdicts under these names

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        required_gates = forge.RequiredGates {names = ["e2e", "health_gate"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
