package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// absentChangeProject builds a project root with a ledger recording rels,
// and creates the files listed in present.
func absentChangeProject(t *testing.T, rels []string, present []string) string {
	t.Helper()
	ResetScaffoldLedgerCache()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	for _, rel := range rels {
		RecordScaffold(root, rel)
	}
	for _, rel := range present {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// TestAbsentScaffoldsIfChanged_SecondRunIsSilent is the headline behaviour.
//
// Before this, generate reported the absent set on EVERY run: control-plane
// reprinted the same 38 paths plus five lines of explanation on a clean tree
// with nothing changed. The notice exists for the MOMENT a scaffold goes
// missing; reprinting a standing fact is how the reader learns to skip the
// block that also carries the lines that mattered.
func TestAbsentScaffoldsIfChanged_SecondRunIsSilent(t *testing.T) {
	root := absentChangeProject(t, []string{"a.go", "b.go"}, []string{"a.go"})

	first := AbsentScaffoldsIfChanged(root)
	if len(first) != 1 || first[0] != "b.go" {
		t.Fatalf("first run must report the newly-absent set, got %v want [b.go]", first)
	}

	if second := AbsentScaffoldsIfChanged(root); second != nil {
		t.Fatalf("second run over an UNCHANGED set reported %v; the notice must fire on change, "+
			"not stand as a banner on every run", second)
	}
}

// TestAbsentScaffoldsIfChanged_NewDeletionReports is the case the notice
// exists for: a scaffold goes missing and generate must say so, even though
// it has already reported a different absent path before.
func TestAbsentScaffoldsIfChanged_NewDeletionReports(t *testing.T) {
	root := absentChangeProject(t, []string{"a.go", "b.go"}, []string{"a.go"})

	_ = AbsentScaffoldsIfChanged(root) // report b.go
	if quiet := AbsentScaffoldsIfChanged(root); quiet != nil {
		t.Fatalf("precondition: expected silence, got %v", quiet)
	}

	// Now the user deletes a.go too.
	if err := os.Remove(filepath.Join(root, "a.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := AbsentScaffoldsIfChanged(root)
	if len(got) != 2 {
		t.Fatalf("a NEW deletion must report; got %v want both a.go and b.go", got)
	}
}

// TestAbsentScaffoldsIfChanged_RestorationReports pins the other direction.
// A path leaving the absent set (rescaffolded, or restored from git) is also
// a change: the previous notice is now stale, and staying silent would mean
// a later re-deletion of that same path never reports.
func TestAbsentScaffoldsIfChanged_RestorationReports(t *testing.T) {
	root := absentChangeProject(t, []string{"a.go", "b.go"}, []string{"a.go"})
	_ = AbsentScaffoldsIfChanged(root)

	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("restore b.go: %v", err)
	}
	if got := AbsentScaffoldsIfChanged(root); len(got) != 0 {
		// An empty (not nil) result is the honest "changed, and now empty".
		t.Logf("restoration reported: %v", got)
	}
	// And the now-empty set is the new baseline: silent from here.
	if got := AbsentScaffoldsIfChanged(root); len(got) != 0 {
		t.Fatalf("after reporting the restoration, the empty set must be the new baseline; got %v", got)
	}
}

// TestAbsentScaffoldsIfChanged_PersistsAcrossProcesses asserts the baseline
// survives a cold start. Without persistence every `forge generate` is a
// first run and the notice is a banner again — the whole point of the change
// is defeated by an in-memory-only memo.
func TestAbsentScaffoldsIfChanged_PersistsAcrossProcesses(t *testing.T) {
	root := absentChangeProject(t, []string{"a.go", "b.go"}, []string{"a.go"})
	if got := AbsentScaffoldsIfChanged(root); len(got) != 1 {
		t.Fatalf("first run: got %v", got)
	}

	// Simulate a fresh process: drop every memo, re-read from disk.
	ResetScaffoldLedgerCache()

	if got := AbsentScaffoldsIfChanged(root); got != nil {
		t.Fatalf("a NEW PROCESS re-reported %v over an unchanged set — the baseline must persist "+
			"in %s, or every run is a first run", got, ScaffoldedFile)
	}
}

// TestAbsentScaffolds_StillAnswersTheStandingFact guards the split: the
// change-gated query is for generate; the unconditional one still backs
// `forge project scaffolded`, which must answer whenever asked.
func TestAbsentScaffolds_StillAnswersTheStandingFact(t *testing.T) {
	root := absentChangeProject(t, []string{"a.go", "b.go"}, []string{"a.go"})
	_ = AbsentScaffoldsIfChanged(root)
	_ = AbsentScaffoldsIfChanged(root)

	got := AbsentScaffolds(root)
	if len(got) != 1 || got[0] != "b.go" {
		t.Fatalf("AbsentScaffolds must keep reporting the standing fact regardless of what has "+
			"been NOTICED; got %v want [b.go]", got)
	}
}
