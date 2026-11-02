"""
Positive test — with a control plane, `forge.DeliveryPolicy {}` resolves
signing and flux_verify to AUDIT: the platform signs every recorded bundle
with the org's key and verifies it server-side, recording the verdict and
never blocking. `enforce` is the env's opt-in (spec.verify, and Promote
refusing an unsigned bundle).
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {}
    delivery_policy = forge.DeliveryPolicy {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_p = output.delivery_policy
assert_signing_audits = _p.signing.mode == "audit" and _p.signing.key == "org_kms"
assert_flux_verify_audits = _p.flux_verify.mode == "audit"
assert_provenance_sbom_enforces = _p.provenance_sbom.mode == "enforce"
