"""
Positive test — keyless Sigstore names its OIDC issuer and subject.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {key = "keyless", oidc_issuer = "https://token.actions.githubusercontent.com", oidc_subject = "https://github.com/acme/api/.*"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_keyless = output.delivery_policy.signing.key == "keyless" and output.delivery_policy.signing.oidc_issuer == "https://token.actions.githubusercontent.com"
assert_no_key_ref = output.delivery_policy.signing.key_ref == None
