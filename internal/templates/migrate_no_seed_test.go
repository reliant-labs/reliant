package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated app's migrate path (pkg/app/migrate.go, from this template)
// must gain NO seed code path: golang-migrate is the only thing that runs
// against non-dev databases, and it must not be able to apply seeds.
//
// This test used to sit beside the seed planner, back when the planner was
// forge-internal and the interesting property was that no shipped binary
// could reach it. The planner now ships in pkg/seedplan so tests can call
// it, which makes reachability the wrong thing to assert — a test IS
// supposed to reach it. What still has to hold, and is asserted here beside
// the template it constrains, is that the MIGRATION path never seeds:
// migrate.go is the one component that runs against production databases.
func TestGeneratedMigrateTemplateHasNoSeedPath(t *testing.T) {
	tmpl := filepath.Join("project", "migrate.go.tmpl")
	data, err := os.ReadFile(tmpl)
	if err != nil {
		t.Skipf("migrate template not found: %v", err)
	}
	if strings.Contains(strings.ToLower(string(data)), "seed") {
		t.Fatal("the generated migrate.go template must contain no seed code path")
	}
}
