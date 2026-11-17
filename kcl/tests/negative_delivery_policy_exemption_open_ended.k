"""
Negative test — a vulnerability exemption must be time-boxed.
"""

# expect: must be a YYYY-MM-DD date

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [forge.VulnExemption {id = "CVE-2026-0001", reason = "unreachable", expires = "never"}]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
