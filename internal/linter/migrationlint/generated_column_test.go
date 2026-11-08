// File: internal/linter/migrationlint/generated_column_test.go
//
// Regression coverage for the defect these tests were written against: two
// forge rules that contradicted each other, with no spelling that satisfied
// both.
//
// `forge lint --read-only-fields` advises fixing a derived column by making
// postgres compute it:
//
//	ALTER TABLE estimates ADD COLUMN total_cents BIGINT
//	  GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED NOT NULL;
//
// unsafe-add-not-null-column then rejected that, and its own remediation
// ("add nullable column, backfill, then SET NOT NULL") is impossible for a
// generated column — postgres refuses an UPDATE against one, so the backfill
// that set-not-null-without-backfill demands cannot be written. A measured
// dogfood run burned ~6 turns on this and escaped only by dropping NOT NULL,
// which projects the Go field as *int64 and makes every consumer nil-check a
// value that arithmetic over NOT NULL columns can never produce.
//
// Both rules exist to catch a scan of a populated table that fails or locks.
// GENERATED ALWAYS ... STORED does neither by that mechanism: postgres
// computes the value for every existing row as part of the ADD COLUMN, so
// the column is fully populated the instant it exists.
//
// The tests below pin the exemption AND its boundary — a plain NOT NULL add
// with no default must still fail, or the fix has gutted the rule instead of
// narrowing it.
package migrationlint

import (
	"strings"
	"testing"
)

// TestAddGeneratedStoredNotNullColumnIsSafe is the core assertion: the exact
// spelling `--read-only-fields` recommends must pass `--migration-safety`.
func TestAddGeneratedStoredNotNullColumnIsSafe(t *testing.T) {
	dir := writeMigration(t, "0001_add_total.up.sql",
		`ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	for _, f := range result.Findings {
		if f.Rule == "unsafe-add-not-null-column" {
			t.Fatalf("GENERATED ALWAYS ... STORED populates every existing row, so the add cannot "+
				"fail the way a plain NOT NULL add does; got %#v", f)
		}
	}
}

// TestSetNotNullOnGeneratedColumnIsSafe covers the split spelling an author
// reaches for after the first one is rejected. It is the arm with no escape
// at all: a generated column cannot be UPDATEd, so the backfill
// set-not-null-without-backfill asks for does not exist as valid SQL.
func TestSetNotNullOnGeneratedColumnIsSafe(t *testing.T) {
	dir := writeMigration(t, "0001_split_total.up.sql", `
ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED;
ALTER TABLE estimates ALTER COLUMN total_cents SET NOT NULL;
`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no findings for a generated column, got %#v", result.Findings)
	}
}

// TestGeneratedColumnExemptionSpellingVariants pins the shapes the exemption
// must recognize. A regex that only matched the canonical one-line form would
// leave the multi-line and quoted-identifier spellings still trapped, which is
// the same dead end with extra steps.
func TestGeneratedColumnExemptionSpellingVariants(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "lowercase",
			content: `alter table estimates add column total_cents bigint generated always as (a + b) stored not null;`,
		},
		{
			name: "multiline",
			content: `ALTER TABLE estimates
  ADD COLUMN total_cents BIGINT
    GENERATED ALWAYS AS (subtotal_cents + tax_cents)
    STORED
    NOT NULL;`,
		},
		{
			name:    "quoted_identifier",
			content: `ALTER TABLE "estimates" ADD COLUMN "total_cents" BIGINT GENERATED ALWAYS AS ("a" + "b") STORED NOT NULL;`,
		},
		{
			name:    "not_null_before_generated",
			content: `ALTER TABLE estimates ADD COLUMN total_cents BIGINT NOT NULL GENERATED ALWAYS AS (a + b) STORED;`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMigration(t, "0001_generated.up.sql", tc.content)

			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatalf("LintMigrationsDir() error = %v", err)
			}
			if len(result.Findings) != 0 {
				t.Fatalf("expected no findings, got %#v", result.Findings)
			}
		})
	}
}

// TestPlainNotNullAddStillFailsAlongsideGeneratedColumn is the boundary. The
// exemption is per-statement, so a file that contains BOTH a legitimate
// generated column and a genuinely unsafe plain add must still report the
// plain one. An exemption that leaked to the whole file would silently retire
// the rule for every migration that happens to add a generated column.
func TestPlainNotNullAddStillFailsAlongsideGeneratedColumn(t *testing.T) {
	dir := writeMigration(t, "0001_mixed.up.sql", `
ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED NOT NULL;
ALTER TABLE estimates ADD COLUMN notes text NOT NULL;
`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
	if len(result.Findings) != 1 {
		t.Fatalf("expected exactly the plain-add finding, got %#v", result.Findings)
	}
}

// TestGeneratedVirtualColumnIsNotExempt guards the narrowness of the
// exemption in the other direction. Only STORED is safe by the argument
// above; postgres 18's GENERATED ... VIRTUAL computes on read, and the
// exemption must not silently widen to cover a form the reasoning never
// covered.
func TestGeneratedVirtualColumnIsNotExempt(t *testing.T) {
	dir := writeMigration(t, "0001_virtual.up.sql",
		`ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (a + b) VIRTUAL NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
}

// TestIdentityColumnStillFlagged keeps GENERATED ALWAYS AS IDENTITY out of
// the exemption. It shares the leading keywords but not the reasoning: there
// is no expression over existing columns, and the sequence-backed add is a
// different operation entirely.
func TestIdentityColumnStillFlagged(t *testing.T) {
	dir := writeMigration(t, "0001_identity.up.sql",
		`ALTER TABLE estimates ADD COLUMN seq BIGINT GENERATED ALWAYS AS IDENTITY NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
}

// TestSetNotNullOnPlainColumnStillFailsAlongsideGeneratedColumn is the
// SET NOT NULL twin of the boundary test above: knowing that total_cents is
// generated must not excuse an unbackfilled SET NOT NULL on a plain column in
// the same file.
func TestSetNotNullOnPlainColumnStillFailsAlongsideGeneratedColumn(t *testing.T) {
	dir := writeMigration(t, "0001_mixed_set.up.sql", `
ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED;
ALTER TABLE estimates ALTER COLUMN total_cents SET NOT NULL;
ALTER TABLE estimates ALTER COLUMN notes SET NOT NULL;
`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "set-not-null-without-backfill", SeverityError)
	if len(result.Findings) != 1 {
		t.Fatalf("expected exactly the plain-column finding, got %#v", result.Findings)
	}
	if got := result.Findings[0].Message; !strings.Contains(got, "notes") {
		t.Fatalf("the surviving finding must name the PLAIN column, got %q", got)
	}
}

// TestSetNotNullOnGeneratedColumnAcrossFilesIsStillFlagged pins the limit of
// what the linter can know. The generated-ness of a column is learned from the
// ADD COLUMN statement; a SET NOT NULL in a LATER migration file has no such
// evidence in scope, and guessing would be worse than the finding. The author
// has the file-level pragma for that case.
func TestSetNotNullOnGeneratedColumnAcrossFilesIsStillFlagged(t *testing.T) {
	dir := writeMigration(t, "0002_set_not_null.up.sql",
		`ALTER TABLE estimates ALTER COLUMN total_cents SET NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "set-not-null-without-backfill", SeverityError)
}

// TestReadOnlyFieldsAdviceIsAcceptedByMigrationSafety is the catch-22 itself,
// as one assertion. It lints the literal SQL that `forge lint
// --read-only-fields` prints, with its placeholders filled in. If either rule
// is edited so the two disagree again, this fails — which is the only guard
// that generalizes, since each rule is individually defensible in isolation
// and it was their INTERSECTION that had no satisfiable spelling.
func TestReadOnlyFieldsAdviceIsAcceptedByMigrationSafety(t *testing.T) {
	// From readOnlyFieldFixHint in internal/cli/lint/lint_read_only_fields.go:
	//   ALTER TABLE <table> ADD COLUMN <col> <type> GENERATED ALWAYS AS (<expression>) STORED NOT NULL
	dir := writeMigration(t, "0001_read_only_advice.up.sql",
		`ALTER TABLE estimates ADD COLUMN total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED NOT NULL;`)

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("--read-only-fields recommends this exact spelling; --migration-safety must accept it, got %#v",
			result.Findings)
	}
}

// ── per-rule remediation ────────────────────────────────────────────────────

// TestRemediationForNamesTheRulesOwnHatch guards against the advice that was
// attached before: every migration-safety failure printed the DESTRUCTIVE
// remediation, so an author whose NOT NULL add was rejected was pointed at
// `-- forge:allow-destructive` and at the allowed_destructive allowlist,
// neither of which silences a NOT NULL finding. That is what sent the audited
// run into forge.yaml, where the partial `database:` block then disabled the
// whole check.
func TestRemediationForNamesTheRulesOwnHatch(t *testing.T) {
	for _, rule := range []string{"unsafe-add-not-null-column", "set-not-null-without-backfill"} {
		got := RemediationFor(rule)
		if !strings.Contains(got, "forge:allow-unsafe-not-null") {
			t.Errorf("RemediationFor(%q) must name the pragma that actually silences it, got %q", rule, got)
		}
		if strings.Contains(got, "allowed_destructive") {
			t.Errorf("RemediationFor(%q) points at the destructive allowlist, which does not silence it: %q", rule, got)
		}
	}
	if got := RemediationFor("destructive-change"); !strings.Contains(got, "forge:allow-destructive") {
		t.Errorf("destructive-change lost its own remediation: %q", got)
	}
}

// TestPrimaryRemediationRefusesToPickForAMixedBatch pins the batch behaviour.
// The CLI prints one Fix line under all findings, so a batch spanning rules
// must not be given any single rule's hatch — that is the same wrong-advice
// failure, just harder to notice.
func TestPrimaryRemediationRefusesToPickForAMixedBatch(t *testing.T) {
	single := []Finding{{Rule: "unsafe-add-not-null-column", Severity: SeverityError}}
	if got := PrimaryRemediation(single); got != UnsafeNotNullRemediation {
		t.Errorf("single-rule batch should get that rule's text, got %q", got)
	}

	mixed := []Finding{
		{Rule: "unsafe-add-not-null-column", Severity: SeverityError},
		{Rule: "destructive-change", Severity: SeverityError},
	}
	got := PrimaryRemediation(mixed)
	if !strings.HasPrefix(got, MixedRemediation) || got == DestructiveChangeRemediation || got == UnsafeNotNullRemediation {
		t.Errorf("mixed batch should refuse to pick one rule's hatch, got %q", got)
	}
	// It names the hatch of each rule present, and of no other.
	for _, want := range []string{"forge:allow-destructive", "forge:allow-unsafe-not-null"} {
		if !strings.Contains(got, want) {
			t.Errorf("mixed batch advice lacks %q for a rule it holds: %q", want, got)
		}
	}
	if strings.Contains(got, "backward-compatible") {
		t.Errorf("mixed batch advice names a hatch for a rule it does not hold: %q", got)
	}

	// Warnings do not gate the build, so they must not steer the fix line
	// printed for the errors that do.
	warnNoise := []Finding{
		{Rule: "volatile-default", Severity: SeverityWarn},
		{Rule: "destructive-change", Severity: SeverityError},
	}
	if got := PrimaryRemediation(warnNoise); got != DestructiveChangeRemediation {
		t.Errorf("a non-gating warning should not make the batch read as mixed, got %q", got)
	}
}

// ── the general-case pragma ─────────────────────────────────────────────────

// TestAllowUnsafeNotNullPragmaSilencesBothRules covers the case the generated-
// column exemption does NOT: an author who knows the table is empty, or whose
// column was made generated in an earlier migration. Before this, the
// destructive-change rule had a per-file opt-out and these two had none, so
// the only escape from a false positive was editing forge.yaml — which is the
// escape hatch that turned out to disable the whole check (see N4).
func TestAllowUnsafeNotNullPragmaSilencesBothRules(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "add_column",
			content: "-- forge:allow-unsafe-not-null (table is empty at this point)\nALTER TABLE users ADD COLUMN name text NOT NULL;",
		},
		{
			name:    "set_not_null",
			content: "-- forge:allow-unsafe-not-null\nALTER TABLE users ALTER COLUMN email SET NOT NULL;",
		},
		{
			name:    "forge_safety_form",
			content: "-- forge-safety: allow-unsafe-not-null — new table\nALTER TABLE users ADD COLUMN name text NOT NULL;",
		},
		{
			name:    "uppercase_form",
			content: "--  FORGE:ALLOW-UNSAFE-NOT-NULL\nALTER TABLE users ADD COLUMN name text NOT NULL;",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMigration(t, "0001_pragma.up.sql", tc.content)

			result, err := LintMigrationsDir(dir, DefaultConfig())
			if err != nil {
				t.Fatalf("LintMigrationsDir() error = %v", err)
			}
			if len(result.Findings) != 0 {
				t.Fatalf("expected the pragma to silence the finding, got %#v", result.Findings)
			}
		})
	}
}

// TestAllowUnsafeNotNullPragmaDoesNotSilenceOtherRules keeps the new pragma
// as narrow as its name, mirroring the guard the destructive pragma already
// has. A too-broad pragma is how one acknowledged exception quietly becomes a
// whole file nobody checks.
func TestAllowUnsafeNotNullPragmaDoesNotSilenceOtherRules(t *testing.T) {
	dir := writeMigration(t, "0001_pragma_plus_destructive.up.sql",
		"-- forge:allow-unsafe-not-null\nALTER TABLE users ADD COLUMN name text NOT NULL;\nDROP TABLE legacy;")

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "destructive-change", SeverityError)
	for _, f := range result.Findings {
		if f.Rule == "unsafe-add-not-null-column" {
			t.Fatalf("unsafe-add-not-null-column should be silenced by the pragma; got %#v", f)
		}
	}
}

// TestDestructivePragmaDoesNotSilenceUnsafeNotNull is the reverse guard: the
// two pragmas must stay distinct, or the older one silently inherits the new
// one's power in every migration that already carries it.
func TestDestructivePragmaDoesNotSilenceUnsafeNotNull(t *testing.T) {
	if testing.Short() {
		t.Skip("LintMigrationsDir shells out to git for every fixture; runs in task test")
	}
	dir := writeMigration(t, "0001_destructive_pragma.up.sql",
		"-- forge:allow-destructive\nALTER TABLE users ADD COLUMN name text NOT NULL;")

	result, err := LintMigrationsDir(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("LintMigrationsDir() error = %v", err)
	}
	assertFinding(t, result, "unsafe-add-not-null-column", SeverityError)
}

// TestCompatFindingsAreNotToldToRewriteTheDestructiveMigration: after a
// migration-compat-undeclared or -reason finding, the closing line said to
// rewrite "the destructive migration" or add "-- forge:allow-destructive" —
// which settles neither (reliant, adopting #777). Each compatibility rule has
// its own fix, alone and in a batch.
func TestCompatFindingsAreNotToldToRewriteTheDestructiveMigration(t *testing.T) {
	for _, rule := range []string{RuleCompatUndeclared, RuleCompatReason, RuleCompatConflict} {
		for name, batch := range map[string][]Finding{
			"alone": {{Rule: rule, Severity: SeverityError}},
			"with another compat rule": {
				{Rule: rule, Severity: SeverityError},
				{Rule: RuleCompatReason, Severity: SeverityError},
				{Rule: RuleCompatUndeclared, Severity: SeverityError},
			},
		} {
			got := PrimaryRemediation(batch)
			if strings.Contains(got, "allow-destructive") || strings.Contains(got, "destructive migration") {
				t.Errorf("%s %s: the closing advice points at the destructive-change hatch: %q", rule, name, got)
			}
			if !strings.Contains(got, "forge:backward-compatible") && !strings.Contains(got, "reason") {
				t.Errorf("%s %s: the closing advice does not say how to declare compatibility: %q", rule, name, got)
			}
		}
		if got := RemediationFor(rule); strings.Contains(got, "allow-destructive") {
			t.Errorf("RemediationFor(%s) = %q", rule, got)
		}
	}
}
