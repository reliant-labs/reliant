package cli

import "testing"

// A disown on a RECONCILED path must never be retirement-targetable.
//
// The retire rule infers "absent from the Tier-1 target set ⇒ obsolete", on the
// premise that forge never writes a non-target. That premise holds for ordinary
// scaffold-once files and fails for the two forge edits in place: it splices
// accessors and fields into them as components are discovered, while never
// listing them as Tier-1 targets. Their disown is what stops that splice, so
// retiring it hands a hand-owned composition root back to the reconciler with
// no diff and no notice.
//
// Today these paths also fall through every location bucket to the conservative
// default, so the guard is redundant — which is exactly why it is pinned. Adding
// "internal/app/" to serviceDrivenCodegenPrefixes is an obvious future tidy-up,
// and on its own it would silently un-protect both files.
func TestRetirementTargetable_ReconciledPathsAreNeverTargetable(t *testing.T) {
	// Gates wide open: codegen on, services present, deploy on. Under these
	// conditions a service-driven Go path IS targetable, so a reconciled path
	// staying false proves the guard fires rather than the default.
	ctx := &pipelineContext{HasServices: true}
	targetable := retirementTargetable(ctx)

	for _, p := range reconciledPaths {
		if targetable(p) {
			t.Errorf("%s is retirement-targetable; a disown on a reconciled path must never be retired", p)
		}
	}
}

// The guard must be narrow: exact files, not the internal/app directory. A
// sibling forge genuinely stops owning should still shed its stale disown once
// internal/app is bucketed.
//
// This asserts the guard's own membership rather than the predicate's verdict,
// because today every internal/app path also reaches the conservative default —
// so the predicate cannot distinguish them and a verdict-based test would pass
// for the wrong reason.
func TestReconciledPaths_AreExactFilesNotADirectory(t *testing.T) {
	want := map[string]bool{
		"internal/app/compose.go":   true,
		"internal/app/lifecycle.go": true,
	}
	if len(reconciledPaths) != len(want) {
		t.Fatalf("reconciledPaths = %v; want exactly the two files forge reconciles in place", reconciledPaths)
	}
	for _, p := range reconciledPaths {
		if !want[p] {
			t.Errorf("reconciledPaths contains %q; the guard must name the files forge splices into, not a directory prefix", p)
		}
	}
}
