"""
Negative test — turning signing off alone is refused, because flux_verify
defaults to enforce and would then reject every bundle. The author must say
what happens to verification too; the message names it.
"""

# expect: signing is "off" but flux_verify enforce(s)

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
