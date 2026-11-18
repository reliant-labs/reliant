"""
Negative test — key_ref on a key model that does not use it is a silent no-op.
"""

# expect: does not use it

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {key_ref = "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
