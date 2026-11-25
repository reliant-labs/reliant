"""
Positive test — AUDIT over a missing prerequisite is allowed. With signing off,
an audited admission check only records "unverifiable"; it cannot make a deploy
impossible. Only ENFORCE over a missing prerequisite is refused.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {mode = "off"}
        flux_verify = forge.Control {mode = "off"}
        admission_verify = forge.Control {mode = "audit"}
        signed_promotions = forge.Control {mode = "audit"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_audit_allowed = output.delivery_policy.admission_verify.mode == "audit" and output.delivery_policy.signing.mode == "off"
