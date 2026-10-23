"""
Positive test — a hardened prod, as control-plane's declares it: every control
enforced, one-person approval (the owner is the only approver), a time-boxed
exemption, and lists rendered sorted so the author's order never changes the
shape.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        trusted_builder = forge.TrustedBuilder {mode = "enforce", builders = ["github:acme/api", "cloudbuild:acme-prod/delivery"]}
        signing = forge.Signing {mode = "enforce"}
        provenance_sbom = forge.Control {mode = "enforce"}
        flux_verify = forge.Control {mode = "enforce"}
        admission_verify = forge.Control {mode = "enforce"}
        signed_promotions = forge.Control {mode = "enforce"}
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            exemptions = [
                forge.VulnExemption {id = "GHSA-aaaa-bbbb-cccc", reason = "unreachable: parser is never called", expires = "2027-01-31"}
                forge.VulnExemption {id = "CVE-2026-0001", reason = "fixed in the next base image", expires = "2026-12-01"}
            ]
        }
        bundle_kinds = forge.BundleKinds {mode = "enforce", allow = ["Service", "Deployment", "apps/StatefulSet"], deny = ["rbac.authorization.k8s.io/ClusterRole"]}
        approvals = forge.Approvals {mode = "enforce"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_p = output.delivery_policy
assert_all_enforce = len([m for m in [_p.trusted_builder.mode, _p.signing.mode, _p.provenance_sbom.mode, _p.flux_verify.mode, _p.admission_verify.mode, _p.signed_promotions.mode, _p.vuln_gate.mode, _p.bundle_kinds.mode, _p.approvals.mode] if m != "enforce"]) == 0
assert_builders_sorted = _p.trusted_builder.builders == ["cloudbuild:acme-prod/delivery", "github:acme/api"]
assert_exemptions_sorted_by_id = [e.id for e in _p.vuln_gate.exemptions] == ["CVE-2026-0001", "GHSA-aaaa-bbbb-cccc"]
assert_kinds_sorted = _p.bundle_kinds.allow == ["Deployment", "Service", "apps/StatefulSet"]
assert_one_person_approval = _p.approvals.required == 1 and _p.approvals.approvers == []
