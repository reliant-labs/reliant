"""
Positive test — `forge.DeliveryPolicy {}` resolves every control to its
default: provenance/SBOM (cannot block a deploy) enforces, the controls that
can block it audit, and signing / flux_verify audit with a control plane.
This env has none, so signing and flux_verify resolve OFF: nothing signs a
self-hosted env's bundles yet (positive_delivery_policy_defaults_hosted.k is
the same env with a control plane).
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_p = output.delivery_policy
# no control plane: nothing signs, nothing verifies
assert_signing_off_self_hosted = _p.signing.mode == "off" and _p.signing.key == "org_kms"
assert_flux_verify_off_self_hosted = _p.flux_verify.mode == "off"
# cannot block a deploy: enforce
assert_provenance_sbom_enforces = _p.provenance_sbom.mode == "enforce"
# can block a deploy: audit
assert_trusted_builder_audits = _p.trusted_builder.mode == "audit" and _p.trusted_builder.builders == []
assert_admission_audits = _p.admission_verify.mode == "audit"
assert_signed_promotions_audits = _p.signed_promotions.mode == "audit"
assert_vuln_gate_audits = _p.vuln_gate.mode == "audit"
assert_bundle_kinds_audits = _p.bundle_kinds.mode == "audit" and _p.bundle_kinds.allow == [] and _p.bundle_kinds.deny == []
assert_approvals_audit_one_person = _p.approvals.mode == "audit" and _p.approvals.required == 1 and _p.approvals.approvers == []
# the vuln gate's default policy: KEV and fixable criticals block, highs warn
assert_vuln_default_policy = _p.vuln_gate.block_kev and _p.vuln_gate.fixable_only and _p.vuln_gate.block_severity == "critical" and _p.vuln_gate.warn_severity == "high" and _p.vuln_gate.exemptions == []
