"""
Negative test — a scoped exemption whose location is not an absolute path
inside the artifact.
"""

# expect: must be absolute paths inside the artifact

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [
            forge.VulnExemption {id = "CVE-2026-0001", reason = "a", expires = "2026-12-01", locations = ["usr/local/bin/grype"]}
        ]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
