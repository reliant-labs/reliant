package generator

import "testing"

// Every CI workflow forge emits goes through writeCIScaffold
// (internal/cli/generate_ci.go): written at most once, user-owned from
// birth, never re-emitted. The drift probe must classify them accordingly,
// or a sanctioned edit to a file forge will never rewrite gets reported as
// a hand-edited generated file.
//
// This is not hypothetical. control-plane's .github/workflows/e2e.yml still
// carries the Tier-1 forge:hash marker it was born with under an older
// forge. A one-line fix to it (PR #128, correcting a frontend path forge
// could not re-derive) made `forge ci verify-generated` fail on main
// permanently, and aborted `forge generate` WHOLESALE — including runs that
// had nothing to do with CI. The drift report could name no extension point
// to move the edit to, because none exists and none should: the whole file
// is the user's.
func TestTier2ManagedPaths_CIWorkflowsAreUserOwned(t *testing.T) {
	tier2 := Tier2ManagedPaths()
	for _, p := range []string{
		".github/workflows/ci.yml",
		".github/workflows/e2e.yml",
		".github/workflows/deploy.yml",
		".github/workflows/build-images.yml",
		".github/workflows/proto-breaking.yml",
		".github/dependabot.yml",
	} {
		if !tier2[p] {
			t.Errorf("%s is written by writeCIScaffold (write-once, user-owned) but is not Tier-2 managed — "+
				"editing it would be reported as drift and would abort every `forge generate`", p)
		}
		if !IsTier2Managed(p) {
			t.Errorf("IsTier2Managed(%q) = false; the drift probe must exempt it", p)
		}
	}
}

// The exemption must stay narrow: a genuinely generated file that forge DOES
// rewrite every run has to keep failing the guard, because silently stomping
// a user's edits there is the worse outcome.
func TestTier2ManagedPaths_RealGeneratedOutputStillGuarded(t *testing.T) {
	for _, p := range []string{
		"internal/app/compose.go",
		"internal/db/orm_shared_gen.go",
		"gen/services/widget/v1/widget.pb.go",
	} {
		if IsTier2Managed(p) {
			t.Errorf("%s is forge-owned generated output; exempting it from the drift guard would let a regenerate stomp user edits silently", p)
		}
	}
}

// FilterTier2Managed is what every gate (generate's stomp guard, lint,
// audit, ci verify-generated) runs the raw scan through, so the exemption
// has to survive that call — not just the registry lookup.
func TestFilterTier2Managed_DropsCIWorkflowDrift(t *testing.T) {
	drift := []Tier1DriftEntry{
		{Path: ".github/workflows/e2e.yml"},
		{Path: "internal/app/compose.go"},
	}
	kept := FilterTier2Managed(drift)
	if len(kept) != 1 || kept[0].Path != "internal/app/compose.go" {
		t.Fatalf("want only the real generated file reported as drift, got %+v", kept)
	}
}
