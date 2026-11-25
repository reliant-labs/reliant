package release

import "testing"

// These strings are a wire vocabulary: forge and the control plane refuse and
// record with them, and pipelines branch on them. Changing one is a breaking
// change, never an edit.
func TestVulnGateVocabularyIsStable(t *testing.T) {
	for got, want := range map[string]string{
		VulnGateGateName:         "vuln_gate",
		ProvenanceSBOMGateName:   "provenance_sbom",
		RefusalVulnGateBlocked:   "vuln_gate_blocked",
		RefusalVulnGateUnscanned: "vuln_gate_unscanned",
		RefusalSBOMMissing:       "sbom_missing",
	} {
		if got != want {
			t.Errorf("%q changed; it must stay %q", got, want)
		}
	}
}
