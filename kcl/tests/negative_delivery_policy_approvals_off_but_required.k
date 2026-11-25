"""
Negative test — required = 2 with the control off would never apply.
"""

# expect: off asks nobody

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approvals = forge.Approvals {mode = "off", required = 2, approvers = ["ana@acme.test", "bo@acme.test"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
