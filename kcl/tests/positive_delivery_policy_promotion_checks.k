"""
Positive test — required gates render sorted and default to enforce; an
approval hook renders its url (or argv) and timeout. An env that declares
neither renders neither: both are opt-in.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {}
    delivery_policy = forge.DeliveryPolicy {
        required_gates = forge.RequiredGates {names = ["security-scan", "e2e"]}
        approval_hook = forge.ApprovalHook {url = "https://approvals.acme.dev/forge", timeout_seconds = 20}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_self_hosted = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approval_hook = forge.ApprovalHook {mode = "audit", argv = ["./scripts/approve.sh", "--env", "prod"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_none = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_g = output.delivery_policy.required_gates
_h = output.delivery_policy.approval_hook
_a = _self_hosted.delivery_policy.approval_hook
assert_gates_default_to_enforce = _g.mode == "enforce"
assert_gates_sorted = _g.names == ["e2e", "security-scan"]
assert_hook_url = _h.url == "https://approvals.acme.dev/forge" and _h.mode == "enforce" and _h.timeout_seconds == 20
assert_argv_kept_in_order = _a.argv == ["./scripts/approve.sh", "--env", "prod"] and _a.mode == "audit" and _a.timeout_seconds == 10
assert_absent_when_undeclared = "required_gates" not in _none.delivery_policy and "approval_hook" not in _none.delivery_policy
