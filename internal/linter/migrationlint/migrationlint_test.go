package migrationlint

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestLintMigrationsDirDetectsUnsafeAddNotNullColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	dir := writeMigration(t, "0001_add_name.up.sql", `ALTER TABLE users ADD COLUMN name text NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
}

func TestLintMigrationsDirAllowsBackfillBeforeSetNotNull(t *testing.T) {
	dir := writeMigration(t, "0001_backfill.up.sql", `
ALTER TABLE users ADD COLUMN name text;
UPDATE users SET name = 'unknown' WHERE name IS NULL;
ALTER TABLE users ALTER COLUMN name SET NOT NULL;
`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no findings, got %#v", result.Findings)
	}
}

func TestLintMigrationsDirDetectsSetNotNullWithoutBackfill(t *testing.T) {
	dir := writeMigration(t, "0001_set_not_null.up.sql", `ALTER TABLE users ALTER COLUMN email SET NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "set-not-null-without-backfill", SeverityError)
}

func TestLintMigrationsDirDetectsDestructiveOperations(t *testing.T) {
	dir := writeMigration(t, "0001_drop_column.up.sql", `ALTER TABLE users DROP COLUMN legacy_name;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "destructive-change", SeverityError)
}

// TestLintMigrationsDirHonorsPerFileAllowDestructivePragma pins the ONE
// opt-out for destructive operations: a `-- forge:allow-destructive` comment
// anywhere in the migration silences the destructive-change rule for that
// file alone. There is no forge.yaml allowlist and no second spelling.
func TestLintMigrationsDirHonorsPerFileAllowDestructivePragma(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "forge_colon_form",
			content: "-- forge:allow-destructive (legacy table rename, intentional)\nDROP TABLE legacy;\nCREATE TABLE legacy (id BIGINT PRIMARY KEY);",
		},
		{
			name:    "uppercase_form",
			content: "--  FORGE:ALLOW-DESTRUCTIVE\nDROP TABLE legacy;",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMigration(t, "0001_drop.up.sql", tc.content)

			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatalf("LintMigrationsDir() error = %v", err)
			}
			for _, f := range result.Findings {
				if f.Rule == "destructive-change" {
					t.Fatalf("destructive-change should be silenced by pragma; got %#v", f)
				}
			}
		})
	}
}

// TestLintMigrationsDirDroppedDestructiveAliasNoLongerSilences pins the
// removal of the second spelling: `-- forge-safety: allow-destructive` was an
// alias and is gone, so a migration still carrying it is flagged again rather
// than silently exempt.
func TestLintMigrationsDirDroppedDestructiveAliasNoLongerSilences(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	dir := writeMigration(t, "0001_drop.up.sql",
		"-- forge-safety: allow-destructive — legacy table rename\nDROP TABLE legacy;")
	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "destructive-change", SeverityError)
}

// TestDestructiveRemediationNamesOnlyTheDirective: the fix text must point at
// the one remaining hatch and never at the retired forge.yaml allowlist.
func TestDestructiveRemediationNamesOnlyTheDirective(t *testing.T) {
	if !strings.Contains(DestructiveChangeRemediation, "-- forge:allow-destructive") {
		t.Errorf("remediation must name the directive, got %q", DestructiveChangeRemediation)
	}
	if strings.Contains(DestructiveChangeRemediation, "allowed_destructive") {
		t.Errorf("remediation still names the retired allowlist: %q", DestructiveChangeRemediation)
	}
}

// TestLintMigrationsDirPragmaDoesNotSilenceOtherRules guards against a
// pragma that's too broad — the destructive opt-out must not suppress
// the unsafe-add-not-null-column or volatile-default findings.
func TestLintMigrationsDirPragmaDoesNotSilenceOtherRules(t *testing.T) {
	dir := writeMigration(t, "0001_pragma_plus_unsafe.up.sql",
		"-- forge:allow-destructive\nDROP TABLE legacy;\nALTER TABLE users ADD COLUMN name text NOT NULL;")

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
	for _, f := range result.Findings {
		if f.Rule == "destructive-change" {
			t.Fatalf("destructive-change should be silenced by pragma; got %#v", f)
		}
	}
}

func TestLintMigrationsDirDetectsVolatileDefault(t *testing.T) {
	dir := writeMigration(t, "0001_add_token.up.sql", `ALTER TABLE users ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "volatile-default", SeverityWarn)
}

// TestLintMigrationsDirDetectsVolatileDefaultVariants pins the full set
// of non-deterministic DEFAULT expressions the rule must catch on an
// ADD COLUMN. Each of these assigns an unpredictable (or uniformly
// identical) value to every pre-existing row at backfill time, which is
// the correctness trap the rule exists to flag.
func TestLintMigrationsDirDetectsVolatileDefaultVariants(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	for _, expr := range []string{
		"now()",
		"NOW ()",
		"current_timestamp",
		"CURRENT_TIMESTAMP",
		"clock_timestamp()",
		"statement_timestamp()",
		"transaction_timestamp()",
		"gen_random_uuid()",
		"uuid_generate_v4()",
		"random()",
		"RANDOM()",
	} {
		t.Run(expr, func(t *testing.T) {
			dir := writeMigration(t, "0001_add_col.up.sql",
				"ALTER TABLE users ADD COLUMN c text DEFAULT "+expr+";")

			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatalf("LintMigrationsDir() error = %v", err)
			}
			assertFinding(t, result, "volatile-default", SeverityWarn)
		})
	}
}

// TestLintMigrationsDirAllowsDeterministicDefaults is the negative half
// of the rule: a constant DEFAULT is safe on a populated table and must
// not be flagged, or the rule would be noise on ordinary migrations.
func TestLintMigrationsDirAllowsDeterministicDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	for _, expr := range []string{"0", "false", "'unknown'", "'2020-01-01'::timestamptz"} {
		t.Run(expr, func(t *testing.T) {
			dir := writeMigration(t, "0001_add_col.up.sql",
				"ALTER TABLE users ADD COLUMN c text NOT NULL DEFAULT "+expr+";")

			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatalf("LintMigrationsDir() error = %v", err)
			}
			for _, f := range result.Findings {
				if f.Rule == "volatile-default" {
					t.Fatalf("deterministic DEFAULT %s must not be flagged; got %#v", expr, f)
				}
			}
		})
	}
}

// TestConfigFromProjectEnablesRulesAtProjectDefaults guards the join
// between the two severity vocabularies: forge.yaml spells the warning
// level "warn" (config.effectiveSeverity normalizes onto that spelling)
// while finding.Severity spells it "warning". If the parser stops
// accepting what the config layer emits, the affected rule silently
// stops firing rather than failing loudly — so assert that a
// default-constructed project config actually leaves every rule ARMED,
// and that an explicit "off" is the only thing that disarms one.
func TestConfigFromProjectEnablesRulesAtProjectDefaults(t *testing.T) {
	cfg := ConfigFromProject(config.MigrationSafetyConfig{})
	if severityFor(cfg.VolatileDefault) != SeverityWarn {
		t.Fatalf("volatile-default disarmed at project defaults: %q parsed to %q",
			cfg.VolatileDefault, severityFor(cfg.VolatileDefault))
	}
	if severityFor(cfg.UnsafeAddColumn) != SeverityError {
		t.Fatalf("unsafe-add-column disarmed at project defaults: %q", cfg.UnsafeAddColumn)
	}
	if severityFor(cfg.DestructiveChange) != SeverityError {
		t.Fatalf("destructive-change disarmed at project defaults: %q", cfg.DestructiveChange)
	}

	// A rule explicitly downgraded to a warning must still fire.
	warned := ConfigFromProject(config.MigrationSafetyConfig{DestructiveChange: "warn"})
	if severityFor(warned.DestructiveChange) != SeverityWarn {
		t.Fatalf("destructive_change: warn disarmed the rule: %q parsed to %q",
			warned.DestructiveChange, severityFor(warned.DestructiveChange))
	}

	// "off" is the one dial that disables.
	disabled := ConfigFromProject(config.MigrationSafetyConfig{VolatileDefault: "off"})
	if severityFor(disabled.VolatileDefault) != "" {
		t.Fatalf("volatile_default: off should disable the rule, got %q", severityFor(disabled.VolatileDefault))
	}
}

// TestLintMigrationsDirVolatileDefaultFiresAtProjectDefaults is the
// end-to-end form of the above: the rule must produce a finding when
// driven by project config, not just by DefaultConfig().
func TestLintMigrationsDirVolatileDefaultFiresAtProjectDefaults(t *testing.T) {
	dir := writeMigration(t, "0001_add_token.up.sql",
		`ALTER TABLE users ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();`)

	result, err := LintMigrationsDir(dir, ConfigFromProject(config.MigrationSafetyConfig{}))
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "volatile-default", SeverityWarn)
}

// A down migration is an ERROR by default: forge rolls forward only. Its
// DROP TABLE body must not ALSO trip destructive-change — the finding is that
// the file exists, not what it says.
func TestLintMigrationsDirFlagsDownMigration(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"0001_create_users.up.sql":   `CREATE TABLE users (id TEXT PRIMARY KEY);`,
		"0001_create_users.down.sql": `DROP TABLE users;`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, RuleNoDownMigration, SeverityError)
	if len(result.Findings) != 1 {
		t.Fatalf("want exactly the no-down-migration finding, got %#v", result.Findings)
	}
	if !result.HasErrors() {
		t.Fatal("a down migration must fail the lint")
	}
	if got := RemediationFor(RuleNoDownMigration); !strings.Contains(got, "forward migration") {
		t.Errorf("remediation must point at the roll-forward alternative, got %q", got)
	}
}

// An EMPTY .down.sql is still a down migration — it tells golang-migrate there
// is a way back.
func TestLintMigrationsDirFlagsEmptyDownFile(t *testing.T) {
	dir := writeMigration(t, "0001_x.down.sql", "")
	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, result, RuleNoDownMigration, SeverityError)
}

// Goose one-file migrations: a Down section with SQL is a down migration; a
// Down marker with nothing (or only directives/comments) under it is not.
func TestLintMigrationsDirGooseDownSection(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	cases := map[string]struct {
		body string
		want bool
	}{
		"down with sql": {"-- +goose Up\nALTER TABLE p ADD COLUMN n TEXT;\n\n-- +goose Down\nALTER TABLE p DROP COLUMN n;\n", true},
		"empty down":    {"-- +goose Up\nALTER TABLE p ADD COLUMN n TEXT;\n-- +goose Down\n-- nothing: roll forward\n", false},
		"directives":    {"-- +goose Up\nSELECT 1;\n-- +goose Down\n-- +goose StatementBegin\n-- +goose StatementEnd\n", false},
		"no down":       {"-- +goose Up\nALTER TABLE p ADD COLUMN n TEXT;\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeMigration(t, "20260926000001_add_n.sql", tc.body)
			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			got := false
			for _, f := range result.Findings {
				if f.Rule == RuleNoDownMigration {
					got = true
					if f.Line != 4 && f.Line != 3 {
						t.Errorf("finding should point at the Down marker, got line %d", f.Line)
					}
				}
			}
			if got != tc.want {
				t.Fatalf("flagged=%v, want %v; findings=%#v", got, tc.want, result.Findings)
			}
		})
	}
}

func writeMigration(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertFinding(t *testing.T, result Result, rule string, severity Severity) {
	t.Helper()
	for _, finding := range result.Findings {
		if finding.Rule == rule && finding.Severity == severity && finding.Line > 0 {
			return
		}
	}
	t.Fatalf("expected finding %s/%s with line number, got %#v", rule, severity, result.Findings)
}

// Allowances lists each safety pragma with the rules it silences, the line its
// `--` is on (not a blank line the regex's leading whitespace spanned), and
// the text after it.
func TestAllowancesListsEachPragma(t *testing.T) {
	src := "\n\n-- forge:allow-destructive: dropping the legacy table\nDROP TABLE legacy;\n" +
		"-- forge-safety: allow-unsafe-not-null\nALTER TABLE t ALTER COLUMN c SET NOT NULL;\n" +
		"-- forge:allow-in-transaction: the subscription has no slot\n"
	got := Allowances(src)
	want := []Allowance{
		{Pragma: "forge:allow-destructive", Rules: []string{"destructive-change"}, Line: 3, Reason: "dropping the legacy table"},
		{Pragma: "forge:allow-unsafe-not-null", Rules: []string{"unsafe-add-not-null-column", "set-not-null-without-backfill"}, Line: 5},
		{Pragma: "forge:allow-in-transaction", Rules: []string{"non-transactional-statement", "enum-value-used-before-commit"}, Line: 7, Reason: "the subscription has no slot"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Allowances:\n got %+v\nwant %+v", got, want)
	}

	// The rules each allowance names are the rules its pragma actually
	// silences: they fire without it and not with it.
	cfg := RuleConfig{Enabled: true, UnsafeAddColumn: "error", DestructiveChange: "error", VolatileDefault: "error"}
	bare := "DROP TABLE legacy;\nALTER TABLE t ADD COLUMN a INT NOT NULL;\nALTER TABLE t ALTER COLUMN c SET NOT NULL;\n" +
		"CREATE INDEX CONCURRENTLY i ON t (c);\nALTER TYPE s ADD VALUE 'x';\nINSERT INTO t (s) VALUES ('x');\n"
	fired := map[string]bool{}
	for _, f := range lintMigrationFile("m.up.sql", bare, cfg) {
		fired[f.Rule] = true
	}
	for _, a := range got {
		silenced := map[string]bool{}
		for _, f := range lintMigrationFile("m.up.sql", "-- "+a.Pragma+"\n"+bare, cfg) {
			silenced[f.Rule] = true
		}
		for _, rule := range a.Rules {
			if !fired[rule] || silenced[rule] {
				t.Errorf("%s claims to silence %q: fires without it=%v, fires with it=%v", a.Pragma, rule, fired[rule], silenced[rule])
			}
		}
	}
}
