package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// ScaffoldMayBeBorn is the one predicate every scaffold-once writer asks about
// an ABSENT file. A path the ledger records (deleted by the user, or declined)
// and a path disowned in .forge/disowned.json are both the project's to leave
// absent; only a path forge has never touched may be born.
func TestScaffoldMayBeBorn(t *testing.T) {
	root := absentChangeProject(t, []string{"deleted.go"}, nil)
	DeclineScaffold(root, "declined.go", "not wanted")
	if err := os.MkdirAll(filepath.Join(root, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	disowned := `{"files":{"disowned.go":{"reason":"mine"}}}`
	if err := os.WriteFile(filepath.Join(root, DisownedFile), []byte(disowned), 0o644); err != nil {
		t.Fatal(err)
	}

	for rel, want := range map[string]bool{
		"never-seen.go": true,
		"deleted.go":    false,
		"declined.go":   false,
		"disowned.go":   false,
	} {
		if got := ScaffoldMayBeBorn(root, rel); got != want {
			t.Errorf("ScaffoldMayBeBorn(%q) = %v, want %v", rel, got, want)
		}
	}
}

// ScaffoldOnceDecision is the same answer plus the presence arm, so a
// disowned path is not born through the generate emitters either.
func TestScaffoldOnceDecision_HonoursDisowned(t *testing.T) {
	root := absentChangeProject(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(root, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DisownedFile), []byte(`{"files":{"x.go":{"reason":"mine"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ScaffoldOnceDecision(root, "x.go") {
		t.Fatal("ScaffoldOnceDecision would birth a disowned path")
	}
}
