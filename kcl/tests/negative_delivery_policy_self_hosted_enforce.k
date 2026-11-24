"""
Negative test — an env with no control plane cannot enforce signing: nothing
signs a self-hosted env's bundles yet, so every release would be refused.
"""

# expect: but this env has no control_plane: nothing signs a self-hosted env's bundles yet

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        flux_verify = forge.Control {mode = "enforce"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
