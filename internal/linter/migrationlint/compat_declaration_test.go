package migrationlint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// compatSet is a migration set as lintCompatDeclarations reads it.
func compatSet(files ...string) []compatFile {
	out := make([]compatFile, 0, len(files)/2)
	for i := 0; i+1 < len(files); i += 2 {
		out = append(out, compatFile{path: "db/migrations/" + files[i], content: files[i+1]})
	}
	return out
}

func rulesAt(findings []Finding) map[string]string {
	out := map[string]string{}
	for _, f := range findings {
		out[filepath.Base(f.File)] = f.Rule
	}
	return out
}

func TestCompatDeclarations_RequiredAfterTheBaseline(t *testing.T) {
	set := compatSet(
		"0001_init.up.sql", "CREATE TABLE plans (id INT);",
		"0002_add.up.sql", "-- forge:backward-compatible — additive column old code ignores\nALTER TABLE plans ADD COLUMN x INT;",
		"0003_drop.up.sql", "-- forge:breaks-rollback the previous release stopped reading y\nALTER TABLE plans DROP COLUMN y;",
		"0004_silent.up.sql", "ALTER TABLE plans ADD COLUMN z INT;",
		"0005_bare.up.sql", "-- forge:backward-compatible\nALTER TABLE plans ADD COLUMN w INT;",
		"0006_placeholder.up.sql", "-- forge:backward-compatible — <why the previous release keeps working>\nSELECT 1;",
		"0007_template.up.sql", "--   -- forge:backward-compatible — <why>\n--   -- forge:breaks-rollback <why>\nSELECT 1;",
		"0008_both.up.sql", "-- forge:backward-compatible — x\n-- forge:breaks-rollback y\nSELECT 1;",
	)
	got := rulesAt(lintCompatDeclarations(set, nil, true))
	want := map[string]string{
		"0004_silent.up.sql":      RuleCompatUndeclared,
		"0005_bare.up.sql":        RuleCompatReason,
		"0006_placeholder.up.sql": RuleCompatReason,
		"0007_template.up.sql":    RuleCompatUndeclared, // the examples `migration new` writes are not a declaration
		"0008_both.up.sql":        RuleCompatConflict,
	}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	for file, rule := range want {
		if got[file] != rule {
			t.Errorf("%s: got %q, want %q", file, got[file], rule)
		}
	}
	if _, ok := got["0001_init.up.sql"]; ok {
		t.Error("the first migration is the baseline: no older release runs on what it builds")
	}
}

func TestCompatDeclarations_OffByDefaultExceptTheContradiction(t *testing.T) {
	set := compatSet(
		"0001_init.up.sql", "CREATE TABLE plans (id INT);",
		"0002_silent.up.sql", "ALTER TABLE plans ADD COLUMN z INT;",
		"0003_both.up.sql", "-- forge:backward-compatible — x\n-- forge:breaks-rollback y\nSELECT 1;",
	)
	got := rulesAt(lintCompatDeclarations(set, nil, false))
	if len(got) != 1 || got["0003_both.up.sql"] != RuleCompatConflict {
		t.Errorf("with the requirement off only a contradiction is reported, got %v", got)
	}
}

// An adopting baseline (a squash, a goose cutover) starts the requirement:
// what it replaced never runs again.
func TestCompatDeclarations_StartAfterTheNewestAdoptingBaseline(t *testing.T) {
	set := compatSet(
		"0001_old.up.sql", "SELECT 1;",
		"0002_old.up.sql", "SELECT 1;",
		"20260101000000_baseline.up.sql", "SELECT 1;",
		"20260102000000_new.up.sql", "SELECT 1;",
	)
	got := rulesAt(lintCompatDeclarations(set, []adoptingFile{{version: 20260101000000}}, true))
	if len(got) != 1 || got["20260102000000_new.up.sql"] != RuleCompatUndeclared {
		t.Errorf("only the migration after the adopting baseline needs a declaration, got %v", got)
	}
}

// The requirement is read from forge.yaml through ConfigFromProject, and
// LintMigrationsDir runs it.
func TestLintMigrationsDir_RequireCompatDeclarationFromConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"0001_init.up.sql":   "CREATE TABLE plans (id INT);",
		"0002_silent.up.sql": "ALTER TABLE plans ADD COLUMN z INT;",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, require := range []bool{false, true} {
		cfg := ConfigFromProject(config.MigrationSafetyConfig{RequireCompatDeclaration: require})
		result, err := LintMigrationsDir(dir, cfg)
		if err != nil {
			t.Fatal(err)
		}
		found := rulesAt(result.Findings)["0002_silent.up.sql"] == RuleCompatUndeclared
		if found != require {
			t.Errorf("require_compat_declaration=%v: undeclared reported=%v (%v)", require, found, result.Findings)
		}
	}
}
