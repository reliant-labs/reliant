"""
Negative test — an argv approval hook on an env with a control plane: the
control plane cannot run it, and a client-side answer could be skipped.
"""

# expect: approval_hook names an argv, but this env has a control_plane

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {}
    delivery_policy = forge.DeliveryPolicy {
        approval_hook = forge.ApprovalHook {argv = ["./scripts/approve.sh"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
