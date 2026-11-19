"""
Negative test — a trusted builder enforced with provenance off: the builder is
read from the provenance, so no artifact could pass.
"""

# expect: trusted_builder is "enforce" but provenance_sbom is "off"

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        trusted_builder = forge.TrustedBuilder {mode = "enforce"}
        provenance_sbom = forge.Control {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
