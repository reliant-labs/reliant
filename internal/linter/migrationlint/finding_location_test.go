package migrationlint

import (
	"path/filepath"
	"strings"
	"testing"
)

// A finding names the line its statement is on. `forge db migration new`
// writes a comment header, so a first statement on line 42 used to be
// reported at :1 — and every later one at the line of the previous
// statement's semicolon.
func TestFindingsPointAtTheStatementLine(t *testing.T) {
	header := strings.Repeat("-- header comment\n", 40) + "\n" // lines 1-41
	body := header +
		"ALTER TABLE orders ADD COLUMN tracking_number TEXT NOT NULL;\n" + // line 42
		"ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMPTZ DEFAULT now();\n" + // line 43
		"\n" +
		"/* a block\n   comment */\n" + // lines 45-46
		"ALTER TABLE orders\n" + // line 47
		"  DROP COLUMN legacy;\n"
	dir := writeMigration(t, "0001_add_order_tracking.up.sql", body)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir: %v", err)
	}
	want := map[string]int{
		"unsafe-add-not-null-column": 42,
		"volatile-default":           43,
		"destructive-change":         47,
	}
	for _, f := range result.Findings {
		if line, ok := want[f.Rule]; ok {
			if f.Line != line {
				t.Errorf("%s reported at line %d, want %d (the statement's own line)", f.Rule, f.Line, line)
			}
			delete(want, f.Rule)
		}
	}
	for rule := range want {
		t.Errorf("expected a %s finding, got %+v", rule, result.Findings)
	}
}

// The verdict counts the files the findings are IN. Two findings in one file
// of two used to read "2 findings across 2 migration files".
func TestVerdictCountsTheFilesWithFindings(t *testing.T) {
	dir := t.TempDir()
	writeMigrationIn(t, dir, "0001_create_orders.up.sql", "CREATE TABLE orders (id uuid PRIMARY KEY);\n")
	writeMigrationIn(t, dir, "0002_add_tracking.up.sql",
		"ALTER TABLE orders ADD COLUMN tracking_number TEXT NOT NULL;\n"+
			"ALTER TABLE orders ADD COLUMN shipped_at TIMESTAMPTZ DEFAULT now();\n")

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir: %v", err)
	}
	var located []Finding
	for _, f := range result.Findings {
		if filepath.Base(f.File) == "0002_add_tracking.up.sql" {
			located = append(located, f)
		}
	}
	if len(located) != 2 {
		t.Fatalf("precondition: want both findings in 0002, got %+v", result.Findings)
	}
	text := result.FormatText()
	if !strings.Contains(text, "2 findings in 1 migration file (2 migration files checked)") {
		t.Errorf("verdict miscounts the files with findings:\n%s", text)
	}
}
