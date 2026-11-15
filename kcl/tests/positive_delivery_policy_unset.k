"""
Positive test — an env that declares no delivery_policy renders no
`delivery_policy` key at all, so its shape hashes as it did before the field.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_no_delivery_policy_key = "delivery_policy" not in output
