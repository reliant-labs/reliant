"""
Negative test — a preview expiry that is not an RFC 3339 UTC time.
"""

# expect: must be an RFC 3339 UTC time

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-preview"}

output = forge.render(forge.Bundle {
    project = "demo"
    preview = forge.Preview {of = "preview", expires_at = "next tuesday"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
