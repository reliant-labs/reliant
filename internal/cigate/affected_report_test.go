package cigate

import (
	"slices"
	"testing"
)

// The --json document reports the rows the diff decides beyond the unit
// lane: lockfile-<frontend> rows and paths_go_closure hits.
func TestAffectedDocument_ReportsLockfileAndClosure(t *testing.T) {
	a := Affected{Changed: []string{"web/package.json", "tools/docgen/main.go"},
		ClosureHits: map[string][]string{"docs-generate": {"tools/docgen/main.go"}}}
	a = WithLockfileReport(a, []Check{{Name: "deps-web"}, {Name: LockfileCheckName("web")}, {Name: UnitTests}})
	d := a.Document()
	if !slices.Equal(d.LockfileChecks, []string{"lockfile-web"}) {
		t.Errorf("lockfile_checks = %v", d.LockfileChecks)
	}
	if got := d.ClosureHits["docs-generate"]; !slices.Equal(got, []string{"tools/docgen/main.go"}) {
		t.Errorf("closure_hits = %v", d.ClosureHits)
	}
	if empty := (&Affected{}).Document(); empty.LockfileChecks == nil {
		t.Error("lockfile_checks must always be a list")
	}
}
