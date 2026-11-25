"""
Positive test — the signing key models. A customer-supplied key is a public
reference; keyless names the identity whose signatures are trusted.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {key = "customer", key_ref = "gcpkms://projects/p/locations/global/keyRings/r/cryptoKeys/k"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_customer_key = output.delivery_policy.signing.key == "customer" and output.delivery_policy.signing.key_ref == "gcpkms://projects/p/locations/global/keyRings/r/cryptoKeys/k"
assert_no_oidc = output.delivery_policy.signing.oidc_issuer == None
