package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAuditIngress_FeatureOffSkipsCategory pins the additive-extension
// contract: when features.ingress is off (or unset for cli/library kinds
// where it defaults off), the ingress key is absent from the report
// entirely. Sub-agents that branch on `.ingress` get nil and know the
// subsystem isn't in play, without misreading "status: ok" as "all routes
// wired". (The ingress cross-check itself stays in package cli with the
// real KCL render; this asserts buildAuditReport's gate.)
func TestAuditIngress_FeatureOffSkipsCategory(t *testing.T) {
	dir := t.TempDir()
	// Ingress is experimental — default off. A forge.yaml with no
	// `features.experimental.ingress` opt-in produces the same
	// no-ingress-category shape as the old explicit-disable case.
	yamlBody := `name: t
module_path: github.com/test/t
version: 0.0.1
forge_version: dev
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	markServiceProject(t, dir)
	report, err := buildAuditReport(testFactory(auditAPIConfig{}), dir)
	if err != nil {
		t.Fatalf("buildAuditReport: %v", err)
	}
	if _, ok := report.Categories["ingress"]; ok {
		t.Error("ingress category present despite features.experimental.ingress not opted in")
	}
}
