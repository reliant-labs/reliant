"""
Negative test — an advisory exempted twice with the same scope (lists in a
different order are the same scope).
"""

# expect: names an advisory twice with the same scope

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [
            forge.VulnExemption {id = "CVE-2026-0001", reason = "a", expires = "2026-12-01", locations = ["/a", "/b"]}
            forge.VulnExemption {id = "CVE-2026-0001", reason = "b", expires = "2026-12-02", locations = ["/b", "/a"]}
        ]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
