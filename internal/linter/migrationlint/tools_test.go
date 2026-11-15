package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A rule that joins this package must say which migration tools it holds
// for, or a project on another tool would get it silently (or not at all).
func TestEveryRuleDeclaresItsTools(t *testing.T) {
	ruleConst := regexp.MustCompile(`(?m)^\s*(?:const\s+)?(Rule[A-Z]\w*)\s*=\s*"`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{
		"RuleDestructiveChange": RuleDestructiveChange, "RuleUnsafeAddNotNullColumn": RuleUnsafeAddNotNullColumn,
		"RuleVolatileDefault": RuleVolatileDefault, "RuleSetNotNullWithoutBackfill": RuleSetNotNullWithoutBackfill,
		"RuleNonTransactionalStatement": RuleNonTransactionalStatement, "RuleEnumValueUsedBeforeCommit": RuleEnumValueUsedBeforeCommit,
		"RuleNoDownMigration": RuleNoDownMigration, "RuleDuplicateVersion": RuleDuplicateVersion,
		"RuleNonTimestampVersion": RuleNonTimestampVersion, "RuleVersionBelowMergedHead": RuleVersionBelowMergedHead,
		"RuleCompatConflict": RuleCompatConflict, "RuleCompatUndeclared": RuleCompatUndeclared,
		"RuleCompatReason": RuleCompatReason, "RuleInvalidAdoption": RuleInvalidAdoption,
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ruleConst.FindAllStringSubmatch(string(src), -1) {
			id, known := ids[m[1]]
			if !known {
				t.Errorf("%s declares %s: add it to this test's list and give it a row in ruleTools (tools.go)", e.Name(), m[1])
				continue
			}
			if _, ok := ruleTools[id]; !ok {
				t.Errorf("rule %q has no row in ruleTools (tools.go): declare which migration tools it applies to", id)
			}
		}
	}
	// Every rule a finding is raised under, inline spellings included.
	if Rule := regexp.MustCompile(`Rule:\s+"`); Rule.MatchString(mustRead(t, "migrationlint.go")) {
		t.Error("migrationlint.go spells a rule ID inline; use a Rule* constant with a ruleTools row")
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func rulesOf(r Result) map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		out[f.Rule]++
	}
	return out
}

func skippedRules(r Result) map[string]string {
	out := map[string]string{}
	for _, sk := range r.RuleSkips {
		for _, rule := range sk.Rules {
			out[rule] = sk.String()
		}
	}
	return out
}

// goose: the SQL-text rules read the Up section of every *.sql file; the
// Down section is the tool's business; forge's naming, roll-forward and
// declaration rules are reported as skipped, not run.
func TestGooseMigrationsLintTheUpSection(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"00001_users.sql": "-- +goose Up\nCREATE TABLE users (id bigint primary key);\n" +
			"ALTER TABLE users ADD COLUMN email text NOT NULL;\n" +
			"-- +goose Down\nDROP TABLE users;\n",
		"00002_index.sql":       "-- +goose Up\nCREATE TABLE t (id int);\nCREATE INDEX CONCURRENTLY t_id ON t (id);\n",
		"00003_index_no_tx.sql": "-- +goose NO TRANSACTION\n-- +goose Up\nCREATE TABLE u (id int);\nCREATE INDEX CONCURRENTLY u_id ON u (id);\n",
		"00004_go.go":           "package migrations\n",
	})
	r, err := LintMigrationsDir(dir, DefaultConfig().ForTool(config.MigrationToolGoose))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Scanned) != 3 {
		t.Errorf("scanned %v, want the three .sql files", r.Scanned)
	}
	got := rulesOf(r)
	if got[RuleUnsafeAddNotNullColumn] != 1 {
		t.Errorf("the Up section's NOT NULL add must be flagged: %v", got)
	}
	if got[RuleDestructiveChange] != 0 || got[RuleNoDownMigration] != 0 {
		t.Errorf("the Down section is goose's rollback, not linted as a forward migration: %v", got)
	}
	if got[RuleNonTransactionalStatement] != 1 {
		t.Errorf("CONCURRENTLY beside another statement in a transactional goose file is flagged once (not under NO TRANSACTION): %v", got)
	}
	skips := skippedRules(r)
	for _, rule := range []string{RuleNoDownMigration, RuleDuplicateVersion, RuleCompatUndeclared, RuleInvalidAdoption} {
		if !strings.Contains(skips[rule], "skipped (tool: goose)") {
			t.Errorf("%s must be reported as skipped (tool: goose), got %q", rule, skips[rule])
		}
	}
	if _, ok := skips[RuleDestructiveChange]; ok {
		t.Error("an SQL-text rule must run under every tool")
	}
	if !strings.Contains(r.FormatText(), "skipped (tool: goose)") {
		t.Errorf("the text report must say what did not run:\n%s", r.FormatText())
	}
}

// flyway: versioned and repeatable scripts are linted, undo scripts are not,
// and the transaction rules are the tool's call.
func TestFlywayMigrationsLint(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"V1__init.sql":   "CREATE TABLE t (id int);\nALTER TABLE t ADD COLUMN c text NOT NULL;\nCREATE INDEX CONCURRENTLY t_c ON t (c);\n",
		"U1__init.sql":   "DROP TABLE t;\n",
		"R__views.sql":   "CREATE OR REPLACE VIEW v AS SELECT 1;\n",
		"atlas.sum.json": "{}",
	})
	r, err := LintMigrationsDir(dir, DefaultConfig().ForTool(config.MigrationToolFlyway))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Scanned) != 2 {
		t.Errorf("scanned %v, want V1__ and R__", r.Scanned)
	}
	got := rulesOf(r)
	if got[RuleUnsafeAddNotNullColumn] != 1 || got[RuleDestructiveChange] != 0 || got[RuleNonTransactionalStatement] != 0 {
		t.Errorf("findings = %v; want the NOT NULL add only", got)
	}
	if !strings.Contains(skippedRules(r)[RuleNonTransactionalStatement], "skipped (tool: flyway)") {
		t.Errorf("the transaction rules must be reported as skipped under flyway: %+v", r.RuleSkips)
	}
}

// A directory with nothing the tool applies is a skip, never a pass.
func TestForeignToolEmptyDirIsASkip(t *testing.T) {
	r, err := LintMigrationsDir(writeMigrations(t, map[string]string{"README.md": "x"}), DefaultConfig().ForTool(config.MigrationToolDbmate))
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped == "" || !strings.HasPrefix(r.FormatText(), "⏭️") {
		t.Errorf("an empty dir must be a skip: %q", r.FormatText())
	}
}

// forge bug c8b8c6e9: goose-shaped files linted as forge's read as zero
// *.up.sql and passed. They are a skip that names the fix.
func TestForgeToolOverAnotherToolsFilesIsASkip(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for forge's version rules; runs in task test")
	}
	dir := writeMigrations(t, map[string]string{"00001_users.sql": "-- +goose Up\nCREATE TABLE users (id int);\n"})
	r, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped == "" || !strings.Contains(r.Skipped, "database.migrations.tool") {
		t.Errorf("want a skip naming database.migrations.tool, got %q (findings %v)", r.Skipped, r.Findings)
	}
}

func TestRuleAppliesTo(t *testing.T) {
	if ok, _ := RuleAppliesTo(RuleDestructiveChange, config.MigrationToolCustom); !ok {
		t.Error("destructive-change holds for any tool")
	}
	if ok, why := RuleAppliesTo(RuleNoDownMigration, config.MigrationToolGolangMigrate); ok || why == "" {
		t.Error("no-down-migration is forge's policy only, with a reason")
	}
	if ok, _ := RuleAppliesTo(RuleNoDownMigration, ""); !ok {
		t.Error("an empty tool is forge")
	}
}
