"""
Positive test — every control off is a valid declaration (a customer's choice).
Nothing is left to verify, so no combination is impossible.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        trusted_builder = forge.TrustedBuilder {mode = "off"}
        signing = forge.Signing {mode = "off"}
        provenance_sbom = forge.Control {mode = "off"}
        flux_verify = forge.Control {mode = "off"}
        admission_verify = forge.Control {mode = "off"}
        signed_promotions = forge.Control {mode = "off"}
        vuln_gate = forge.VulnPolicy {mode = "off"}
        bundle_kinds = forge.BundleKinds {mode = "off"}
        approvals = forge.Approvals {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_all_off = len([m for m in [output.delivery_policy.trusted_builder.mode, output.delivery_policy.signing.mode, output.delivery_policy.provenance_sbom.mode, output.delivery_policy.flux_verify.mode, output.delivery_policy.admission_verify.mode, output.delivery_policy.signed_promotions.mode, output.delivery_policy.vuln_gate.mode, output.delivery_policy.bundle_kinds.mode, output.delivery_policy.approvals.mode] if m != "off"]) == 0
