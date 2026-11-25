package release

// The promotion checks an env's delivery policy declares for what a release
// ships: vuln_gate (its known vulnerabilities) and provenance_sbom (an SBOM
// for every artifact it ships). Every place that enforces them — forge for an
// env whose ledger is on this machine, the control plane for an env it holds
// — records and refuses with these strings, so a pipeline branches on one
// vocabulary whichever ledger judged the promotion.

// The names the enforcing ledger records each check's verdict under, on the
// promotion. A caller-supplied gate of either name is refused where the
// policy runs the check: a caller that could write it could forge the
// ledger's own verdict.
const (
	VulnGateGateName       = "vuln_gate"
	ProvenanceSBOMGateName = "provenance_sbom"
)

// The refusal reasons of the two checks: the codes a pipeline branches on.
// Each refuses only under an enforce the warn-first rollout grades as
// enforce (the lint rules vuln-gate and sbom-required).
const (
	// RefusalVulnGateBlocked: a finding the env's vuln_gate blocks — on
	// CISA's KEV list, or at or above block_severity (with a fixed version,
	// under fixable_only) — that no unexpired exemption covers.
	RefusalVulnGateBlocked = "vuln_gate_blocked"
	// RefusalVulnGateUnscanned: an artifact with no SBOM or no scan, or
	// findings from a vulnerability database older than five days. Could
	// not determine is never a pass.
	RefusalVulnGateUnscanned = "vuln_gate_unscanned"
	// RefusalSBOMMissing: provenance_sbom is enforce, and an artifact the
	// release ships has no SBOM.
	RefusalSBOMMissing = "sbom_missing"
)
