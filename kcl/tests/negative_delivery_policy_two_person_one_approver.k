"""
Negative test — a two-person rule with one declared approver.
"""

# expect: only 1 approver(s) are declared

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approvals = forge.Approvals {mode = "enforce", required = 2, approvers = ["ana@acme.test"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
