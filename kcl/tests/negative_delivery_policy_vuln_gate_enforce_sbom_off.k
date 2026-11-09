"""
Negative test — a vulnerability gate enforced with the SBOM off: it scans the
SBOM.
"""

# expect: vuln_gate is "enforce" but provenance_sbom is "off"

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {mode = "enforce"}
        provenance_sbom = forge.Control {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
