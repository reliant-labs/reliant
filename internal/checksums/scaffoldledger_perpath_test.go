package checksums_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/mergeproperty"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The ledger is one entry file per path, so two branches that scaffold
// different files — each lane adopting its own service, the P3b shape —
// merge with plain git into exactly the ledger forge writes for both.
func TestScaffoldLedger_PlainGitMergeOfIndependentBirths(t *testing.T) {
	testkit.Slow(t, "execs git")
	births := map[string][]string{
		"base":   {".github/workflows/ci.yml", "internal/handlers/system/a_test.go"},
		"ours":   {".github/workflows/ci.yml", "internal/handlers/question/q_test.go", "internal/handlers/system/a_test.go"},
		"theirs": {".github/workflows/ci.yml", "internal/handlers/run/r_test.go"},
		"merged": {".github/workflows/ci.yml", "internal/handlers/question/q_test.go", "internal/handlers/run/r_test.go"},
	}
	mergeproperty.AssertPlainMergeIsRenderTree(t, checksums.ScaffoldedDir, func(dir, variant string) {
		checksums.ResetScaffoldLedgerCache()
		want := map[string]bool{}
		for _, p := range births[variant] {
			want[p] = true
			checksums.RecordScaffold(dir, p)
		}
		for _, p := range checksums.ScaffoldLedgerPaths(dir) {
			if !want[p] {
				checksums.ForgetScaffold(dir, p)
			}
		}
		checksums.ResetScaffoldLedgerCache()
	})
}

// Each recorded path is one entry file; a plain birth's records nothing but
// the birth, an absent path keeps its reported mark, and forgetting every
// path leaves no directory.
func TestScaffoldLedger_EntryFilesPerPath(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n") // present; gone.go is the absent one
	write("b.go", "package a\n")
	checksums.ResetScaffoldLedgerCache()
	for _, p := range []string{"a.go", "gone.go", "b.go"} {
		checksums.RecordScaffold(root, p)
	}
	if got := checksums.AbsentScaffoldsIfChanged(root); len(got) != 1 || got[0] != "gone.go" {
		t.Fatalf("absent set = %v, want [gone.go]", got)
	}
	checksums.ResetScaffoldLedgerCache()

	for _, p := range []string{"a.go", "gone.go", "b.go"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(checksums.ScaffoldEntryPath(p)))); err != nil {
			t.Errorf("no entry file for %s: %v", p, err)
		}
		if !checksums.ScaffoldRecorded(root, p) {
			t.Errorf("%s not recorded", p)
		}
	}
	// A plain birth's entry records nothing but the birth: no clock, so
	// two branches that both scaffold a path write the same bytes.
	a, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(checksums.ScaffoldEntryPath("a.go"))))
	if string(a) != "{}\n" {
		t.Errorf("a.go's entry = %q, want {}", a)
	}
	gone, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(checksums.ScaffoldEntryPath("gone.go"))))
	if !strings.Contains(string(gone), `"absent_reported":true`) {
		t.Errorf("gone.go's entry lost its reported-absent mark: %s", gone)
	}
	// The absent baseline survives: an unchanged absent set is not re-reported.
	if got := checksums.AbsentScaffoldsIfChanged(root); got != nil {
		t.Errorf("re-reported an unchanged absent set: %v", got)
	}

	// Forgetting a path removes its entry file and the directories it emptied.
	checksums.ForgetScaffold(root, "gone.go")
	checksums.ForgetScaffold(root, "a.go")
	checksums.ForgetScaffold(root, "b.go")
	if _, err := os.Stat(filepath.Join(root, checksums.ScaffoldedDir)); !os.IsNotExist(err) {
		t.Errorf("an empty ledger must leave no directory (stat err=%v)", err)
	}
}

// A ledger already in the current shape is left alone by the per-run
// convergence `forge generate` calls.
func TestPersistLedgerMigrations_LeavesACurrentLedgerAlone(t *testing.T) {
	root := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	checksums.RecordScaffold(root, "a.go")
	checksums.ResetScaffoldLedgerCache()
	entry := filepath.Join(root, filepath.FromSlash(checksums.ScaffoldEntryPath("a.go")))

	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatal(err)
	}
	checksums.PersistLedgerMigrations(root)
	checksums.ResetScaffoldLedgerCache()
	if st, err := os.Stat(entry); err != nil || !st.ModTime().Equal(old) {
		t.Errorf("a current ledger was rewritten (err %v)", err)
	}
}

func TestScaffoldEntryPathRoundTrip(t *testing.T) {
	for _, rel := range []string{"a.go", ".github/workflows/ci.yml", "frontends/web/package.json"} {
		got, ok := checksums.ScaffoldEntryTarget(checksums.ScaffoldEntryPath(rel))
		if !ok || got != rel {
			t.Errorf("round trip %q: %q, %v", rel, got, ok)
		}
	}
	for _, not := range []string{".forge/scaffolded.json", ".forge/scaffolded/a.go", ".forge/scaffolded/../x.json"} {
		if _, ok := checksums.ScaffoldEntryTarget(not); ok {
			t.Errorf("ScaffoldEntryTarget(%q) accepted", not)
		}
	}
	if !checksums.IsCommittedState(checksums.ScaffoldEntryPath("a.go")) {
		t.Error("a ledger entry is committed state")
	}
}

// The driver merges an entry as one value: a one-sided change wins.
func TestMergeStateFile_ScaffoldEntry(t *testing.T) {
	rel := "svc/" + checksums.ScaffoldEntryPath("a.go")
	base, changed := []byte(`{"scaffolded_at":"x"}`+"\n"), []byte(`{"declined":"no"}`+"\n")
	if got, ok := checksums.MergeStateFile(rel, base, base, changed); !ok || string(got) != string(changed) {
		t.Errorf("theirs-only change: %q %v", got, ok)
	}
	if got, ok := checksums.MergeStateFile(rel, base, changed, base); !ok || string(got) != string(changed) {
		t.Errorf("ours-only change: %q %v", got, ok)
	}
	if _, ok := checksums.MergeStateFile(rel, base, []byte("<<<<<<<\n"), changed); ok {
		t.Error("an unparseable side must fall back to the text merge")
	}
}
