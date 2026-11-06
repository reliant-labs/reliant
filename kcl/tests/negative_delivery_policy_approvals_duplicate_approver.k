"""
Negative test — an approver named twice does not make two people.
"""

# expect: names a person twice

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approvals = forge.Approvals {mode = "enforce", required = 2, approvers = ["ana@acme.test", "ana@acme.test"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
