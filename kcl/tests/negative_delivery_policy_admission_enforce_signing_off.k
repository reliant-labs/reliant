"""
Negative test — admission verification enforced while signing is off: no image
would carry a signature, so no pod could be admitted.
"""

# expect: signing is "off" but admission_verify

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {mode = "off"}
        flux_verify = forge.Control {mode = "off"}
        admission_verify = forge.Control {mode = "enforce"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
