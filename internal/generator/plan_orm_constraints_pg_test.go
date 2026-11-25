package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// This is the assertion the whole feature rests on: a constraint constant
// whose value does not match what postgres reports at runtime is WORSE than
// no constant, because it reads as authoritative and the branch that
// compares against it never fires.
//
// Nothing short of a real server can settle it. Postgres AUTO-NAMES the
// constraints forge's migrations mostly declare inline — `UNIQUE` on a
// column becomes `<table>_<col>_key`, an inline `CHECK` becomes
// `<table>_<col>_check`, and a second unnamed check on the same column
// becomes `_check1` — and those names exist nowhere in the migration text.
// Deriving them in Go would be forge re-implementing postgres's naming
// rules and then grading its own homework; introspecting them and then
// violating the constraint to hear postgres say the name back is the only
// round trip that can catch a disagreement.
func TestConstraintConstants_MatchWhatPostgresReports(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres; skipped under -short")
	}

	dir := t.TempDir()
	writeConstraintMig(t, dir, "00001_create_jobs.up.sql", `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY
);
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    estimate_id TEXT NOT NULL UNIQUE REFERENCES estimates(id),
    total_cents BIGINT NOT NULL CHECK (total_cents >= 0)
);
`)

	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(dir, "")
	if shadow != nil {
		defer shadow.Close()
	}
	if err != nil {
		t.Fatalf("ApplyAndIntrospectShadowAt: %v", err)
	}

	var jobs schemadef.Table
	for _, tb := range tables {
		if tb.Name == "jobs" {
			jobs = tb
		}
	}
	if jobs.Name == "" {
		t.Fatalf("jobs table missing from introspection: %+v", tables)
	}

	ent := config.PlanEntity{
		Name:        "Job",
		TableName:   "jobs",
		Fields:      []config.PlanEntityField{{Name: "id", Type: "string", PrimaryKey: true}},
		Constraints: codegen.EntityConstraintsToPlan(codegen.EntityConstraintsFromTable(jobs)),
	}
	code := string(renderORMEntity("db", ent, false))

	db := shadow.DB()
	if _, err := db.Exec(`INSERT INTO estimates (id) VALUES ('e1')`); err != nil {
		t.Fatalf("seed estimates: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (id, estimate_id, total_cents) VALUES ('j1', 'e1', 100)`); err != nil {
		t.Fatalf("seed jobs: %v", err)
	}

	// Each case provokes the real violation and asserts the name postgres
	// hands back is the one the generated file published.
	cases := []struct {
		what string
		sql  string
	}{
		{"unique", `INSERT INTO jobs (id, estimate_id, total_cents) VALUES ('j2', 'e1', 100)`},
		{"check", `INSERT INTO jobs (id, estimate_id, total_cents) VALUES ('j3', 'e1', -1)`},
		{"foreign key", `INSERT INTO jobs (id, estimate_id, total_cents) VALUES ('j4', 'nope', 100)`},
	}
	for _, tc := range cases {
		_, execErr := db.Exec(tc.sql)
		if execErr == nil {
			t.Fatalf("%s: statement unexpectedly succeeded; the constraint is not doing its job", tc.what)
		}
		runtimeName := orm.ConstraintName(execErr)
		if runtimeName == "" {
			t.Fatalf("%s: postgres named no constraint in %v", tc.what, execErr)
		}
		if !constantWithValue(code, runtimeName) {
			t.Errorf("%s: postgres reported constraint %q at runtime, but no generated constant carries that value.\nGenerated:\n%s",
				tc.what, runtimeName, constantBlock(code))
		}
	}
}

// A rename in a migration must move BOTH halves together — the value
// postgres reports and the constant forge publishes. If only one moved, a
// service branch would keep compiling against a constraint that no longer
// exists.
func TestConstraintConstants_RenameInMigrationMovesBothHalves(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres; skipped under -short")
	}

	dir := t.TempDir()
	writeConstraintMig(t, dir, "00001_create_jobs.up.sql", `
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    estimate_id TEXT NOT NULL UNIQUE
);
`)
	writeConstraintMig(t, dir, "00002_rename_constraint.up.sql", `
ALTER TABLE jobs RENAME CONSTRAINT jobs_estimate_id_key TO one_job_per_estimate;
`)

	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(dir, "")
	if shadow != nil {
		defer shadow.Close()
	}
	if err != nil {
		t.Fatalf("ApplyAndIntrospectShadowAt: %v", err)
	}

	var jobs schemadef.Table
	for _, tb := range tables {
		if tb.Name == "jobs" {
			jobs = tb
		}
	}
	ent := config.PlanEntity{
		Name:        "Job",
		TableName:   "jobs",
		Fields:      []config.PlanEntityField{{Name: "id", Type: "string", PrimaryKey: true}},
		Constraints: codegen.EntityConstraintsToPlan(codegen.EntityConstraintsFromTable(jobs)),
	}
	code := string(renderORMEntity("db", ent, false))

	if strings.Contains(code, "jobs_estimate_id_key") {
		t.Error("the pre-rename constraint name is still generated; it names nothing in the applied schema")
	}
	if !constantWithValue(code, "one_job_per_estimate") {
		t.Errorf("renamed constraint missing from generated constants:\n%s", constantBlock(code))
	}

	db := shadow.DB()
	if _, err := db.Exec(`INSERT INTO jobs (id, estimate_id) VALUES ('j1', 'e1')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, execErr := db.Exec(`INSERT INTO jobs (id, estimate_id) VALUES ('j2', 'e1')`)
	if execErr == nil {
		t.Fatal("duplicate insert succeeded; the renamed constraint is not enforcing")
	}
	if got := orm.ConstraintName(execErr); got != "one_job_per_estimate" {
		t.Errorf("postgres reported %q, want the renamed constraint — the generated constant would not match", got)
	}
}

func writeConstraintMig(t *testing.T, dir, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

// constantWithValue reports whether the generated code declares SOME
// constraint constant whose value is name. The identifier is deliberately
// not pinned here: these tests are about the VALUE agreeing with postgres,
// and the identifier spelling is pinned by the unit tests.
func constantWithValue(code, name string) bool {
	return regexp.MustCompile(`(?m)^\s*\w*Constraint\w+\s*=\s*"` + regexp.QuoteMeta(name) + `"\s*$`).MatchString(code)
}

// constantBlock extracts the generated constraint constants for a failure
// message, so a mismatch reports what forge DID emit rather than the whole file.
func constantBlock(code string) string {
	var out []string
	for _, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "Constraint") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return "(no constraint constants emitted at all)"
	}
	return strings.Join(out, "\n")
}
