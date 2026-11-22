package generator

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/scaffolds"
)

// TestRenderORMEntityPassesScaffoldOwnershipLint pins the ORM emitter to the
// rule `forge lint` holds every _gen file to: the canonical header AND a
// "// Source:" line. The ORM header used to stop at the forge-owned banner,
// so every freshly scaffolded project reported gen-missing-source once per
// entity — a warning about a file the user is told never to edit.
func TestRenderORMEntityPassesScaffoldOwnershipLint(t *testing.T) {
	code := renderORMEntity("db", constraintTestEntity(nil), false)

	if fs := scaffolds.LintGeneratedHeader("internal/db/job_orm_gen.go", code); len(fs) > 0 {
		t.Fatalf("the generated ORM fails forge's own scaffold-ownership lint: %+v\nheader:\n%s",
			fs, firstNLines(string(code), 6))
	}
	// The Source line has to name THIS table, or it says nothing a reader
	// could act on.
	if !strings.Contains(firstNLines(string(code), 6), "// Source: the APPLIED schema (db/migrations) of table jobs.") {
		t.Errorf("ORM Source line does not name the table it projects:\n%s", firstNLines(string(code), 6))
	}
}

func firstNLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
