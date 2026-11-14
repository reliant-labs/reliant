package migratekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigratekitHasNoSeedPath is the production-safety property that used to
// be asserted against the generated pkg/app/migrate.go template, moved here
// with the code it constrains.
//
// The migration path is the ONE component that runs against production
// databases. Seeding is a dev-fixture concern (pkg/seedplan), and the two must
// never meet: a seed reachable from the migrator is a path by which fixture
// rows reach production. The planner ships as a library now, so "no shipped
// binary can reach it" is no longer the right assertion — a test is supposed
// to reach it. What still has to hold is that THIS package cannot.
//
// Asserted over the package source rather than the import graph because the
// property is about what may be written here at all, and a source-level guard
// catches a hand-rolled seed helper that imports nothing.
func TestMigratekitHasNoSeedPath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var checked int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		if strings.Contains(strings.ToLower(string(body)), "seed") {
			t.Errorf("%s mentions seeding. The migration path runs against production "+
				"databases and must have no seed code path — seeding belongs to "+
				"pkg/seedplan, on the dev side of that line.", name)
		}
	}

	// A guard that silently checked nothing would pass forever after a
	// rename; make the file set itself an assertion.
	if checked == 0 {
		t.Fatal("no non-test .go files found in migratekit — this guard checked nothing")
	}
}
