"""
Negative test — an approval hook URL that is plain http to a remote host, or
carries credentials.
"""

# expect: must be an https URL with no credentials in it

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approval_hook = forge.ApprovalHook {url = "http://approvals.acme.dev/forge"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
