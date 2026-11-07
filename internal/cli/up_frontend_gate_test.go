package cli

import "testing"

// TestFrontendPhaseEnabled pins the rule the up orchestrator's frontend phase
// resolves on: frontends declared in the render are frontends started.
//
// Frontend topology lives in deploy/kcl/<env>/, and there is no forge.yaml
// switch to consult. Gating the phase on anything derived from a forge.yaml
// inventory once silently skipped every KCL-declared frontend — the dev server
// never started, and the only loud symptom was a service that waits on a
// frontend port failing the post-launch readiness gate.
func TestFrontendPhaseEnabled(t *testing.T) {
	declared := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: "web"}}}

	if !frontendPhaseEnabled(declared) {
		t.Error("frontendPhaseEnabled(1 declared frontend) = false, want true")
	}
	if frontendPhaseEnabled(&KCLEntities{}) {
		t.Error("frontendPhaseEnabled(no frontends) = true, want false: a backend-only project has none to start")
	}
	if frontendPhaseEnabled(nil) {
		t.Error("frontendPhaseEnabled(nil entities) = true, want false")
	}
}
