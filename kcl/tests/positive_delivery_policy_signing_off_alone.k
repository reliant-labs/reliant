"""
Positive test — turning signing off alone is accepted: flux_verify defaults
to audit (with a control plane), which only records "unverifiable" and never
blocks, so nothing becomes impossible to pass.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {}
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_signing_off = output.delivery_policy.signing.mode == "off"
assert_flux_verify_audits = output.delivery_policy.flux_verify.mode == "audit"
