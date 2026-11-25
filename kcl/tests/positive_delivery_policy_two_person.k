"""
Positive test — the two-person rule is a per-env option: required = 2 with at
least two named approvers.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approvals = forge.Approvals {mode = "enforce", required = 2, approvers = ["bo@acme.test", "ana@acme.test"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_two_person = output.delivery_policy.approvals.required == 2 and output.delivery_policy.approvals.mode == "enforce"
assert_approvers_sorted = output.delivery_policy.approvals.approvers == ["ana@acme.test", "bo@acme.test"]
