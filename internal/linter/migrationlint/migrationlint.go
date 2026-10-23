package migrationlint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/migrationgit"
)

// Severity and Finding now live in the shared internal/linter/finding
// package. SeverityWarn is kept as a package-local alias for source
// compatibility, but it now resolves to the canonical "warning"
// spelling (previously this package spelled it "warn" — the only
// migration-visible change is that FormatText now renders "warning").
type (
	// Severity is the shared finding severity vocabulary, re-exported
	// under this package's historical spelling.
	Severity = finding.Severity
	// Finding is the shared linter finding shape, re-exported under this
	// package's historical spelling.
	Finding = finding.Finding
)

// Severity enum values (aliases onto the canonical single-spelling set).
const (
	SeverityError = finding.SeverityError
	SeverityWarn  = finding.SeverityWarning
)

// Result aggregates every Finding from a migration-dir lint pass.
// Distinct type (not an alias) so migrationlint keeps its own FormatText.
//
// Findings alone cannot carry a verdict: a pass that read zero files
// produces zero findings, and a report built from findings alone renders
// that identically to a clean pass over fifty migrations. `forge ci
// migration-safety` shipped exactly that message — `✅ No migration
// safety warnings!` over an empty directory, and (because the directory
// path is resolved relative to the process cwd) over a nonexistent one
// whenever the command ran from a subdirectory. Scanned and Skipped are
// the evidence that makes the green falsifiable.
type Result struct {
	Findings []Finding
	// Dir is the directory the pass walked, exactly as the caller named
	// it. Printed on every verdict so a wrong path is visible instead of
	// masquerading as a clean tree.
	Dir string
	// Scanned lists every *.up.sql file actually read. A green with an
	// empty Scanned means nothing was checked, and FormatText says so.
	Scanned []string
	// Skipped, when non-empty, is the reason the pass examined nothing —
	// rule set disabled, directory absent, or directory empty. Phrased as
	// a clause that completes "nothing was checked: ...".
	Skipped string
}

// HasErrors reports whether any Finding is of severity Error.
func (r Result) HasErrors() bool {
	for _, finding := range r.Findings {
		if finding.Severity == SeverityError {
			return true
		}
	}
	return false
}

// FormatText renders the result as a human-readable text report.
//
// The verdict always names how many migration files were read and where
// they were read from, so "clean" and "checked nothing" can never render
// the same way. A pass that examined nothing gets ⏭️, not ✅ — a check
// that looked at no files has not earned a green.
func (r Result) FormatText() string {
	if r.Skipped != "" {
		return fmt.Sprintf("⏭️  migration safety: nothing was checked — %s\n", r.Skipped)
	}

	var b strings.Builder
	for _, finding := range r.Findings {
		fmt.Fprintf(&b, "%s:%d: %s [%s] %s\n", finding.File, finding.Line, finding.Severity, finding.Rule, finding.Message)
	}
	if len(r.Findings) == 0 {
		fmt.Fprintf(&b, "✅ migration safety: %s clean, no warnings\n", plural(len(r.Scanned), "migration file"))
		return b.String()
	}
	checked := ""
	if len(r.Scanned) > 0 {
		checked = fmt.Sprintf(" (%s checked)", plural(len(r.Scanned), "migration file"))
	}
	fmt.Fprintf(&b, "%s in %s%s\n", plural(len(r.Findings), "finding"), plural(r.filesWithFindings(), "migration file"), checked)
	return b.String()
}

// filesWithFindings counts the distinct files the findings are in. The
// verdict used to put the finding count beside the number of files SCANNED
// ("2 findings across 2 migration files"), which read as one finding per file
// when both were in the same one.
func (r Result) filesWithFindings() int {
	files := map[string]bool{}
	for _, finding := range r.Findings {
		files[finding.File] = true
	}
	return len(files)
}

// plural renders "1 thing" / "3 things" so counts read naturally in the
// verdict lines without every call site repeating the ternary.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// RuleConfig is the per-rule severity configuration consumed by
// LintMigrationsDir. Mirrors config.MigrationSafetyConfig but is
// pre-resolved (no nil enabled, severities as strings).
type RuleConfig struct {
	Enabled           bool
	UnsafeAddColumn   string
	DestructiveChange string
	VolatileDefault   string
	// RequireCompatDeclaration turns on migration-compat-undeclared and
	// migration-compat-reason (compat_declaration.go).
	RequireCompatDeclaration bool
}

// ConfigFromProject lifts a config.MigrationSafetyConfig into a
// migrationlint.RuleConfig by resolving defaults.
func ConfigFromProject(cfg config.MigrationSafetyConfig) RuleConfig {
	return RuleConfig{
		Enabled:           cfg.IsEnabled(),
		UnsafeAddColumn:   cfg.EffectiveUnsafeAddColumn(),
		DestructiveChange: cfg.EffectiveDestructiveChange(),
		VolatileDefault:   cfg.EffectiveVolatileDefault(),

		RequireCompatDeclaration: cfg.RequireCompatDeclaration,
	}
}

// DefaultConfig returns the migration-lint defaults: all three rules
// at error/error/warn, enabled.
func DefaultConfig() RuleConfig {
	return RuleConfig{
		Enabled:           true,
		UnsafeAddColumn:   "error",
		DestructiveChange: "error",
		VolatileDefault:   "warn",
	}
}

// LintMigrationsDir walks dir for *.up.sql files and returns a Result
// containing every rule violation it finds. Every OTHER .sql file is checked
// for being a down migration (no-down-migration): forge rolls forward only.
//
// The three "examined nothing" outcomes — rules disabled, directory
// absent, directory empty — each set Result.Skipped with the reason and
// the next action, so the caller reports a skip rather than a pass. They
// are NOT errors: a project with no migrations yet is a normal state, and
// failing CI over it would be wrong. What is wrong is calling it clean.
func LintMigrationsDir(dir string, cfg RuleConfig) (Result, error) {
	if !cfg.Enabled {
		return Result{Dir: dir, Skipped: "the rule set is off (database.migration_safety.enabled: false in forge.yaml) — remove that key to turn the checks back on"}, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return Result{Dir: dir, Skipped: fmt.Sprintf("no migrations directory at %s (path is resolved from the current directory — run this from the project root, or set database.migrations_dir in forge.yaml)", dir)}, nil
	}

	var files, rollbackCandidates []string
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".sql") {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".up.sql") {
			files = append(files, path)
		} else {
			// *.down.sql, or a one-file goose migration that may carry a
			// Down section.
			rollbackCandidates = append(rollbackCandidates, path)
		}
		return nil
	}); err != nil {
		return Result{}, err
	}
	sort.Strings(files)
	sort.Strings(rollbackCandidates)

	findings, err := lintDownMigrations(rollbackCandidates)
	if err != nil {
		return Result{}, err
	}

	// Version rules are about the SET of migrations, not any one file, so
	// they run over the whole list rather than inside lintMigrationFile.
	// Both git facts are resolved once, here, because each is a subprocess:
	// the merge-base answers "which of these files are new on this branch",
	// and the default branch's TIP answers "what has every database already
	// recorded". The tip, not the merge-base, is the head a new migration
	// has to clear — a migration that landed on main after this branch was
	// cut has still been applied. See lintVersions.
	repoRoot := migrationgit.RepoRoot(dir)
	mergeBaseMax, haveMergeBase := migrationgit.MergeBaseMax(repoRoot, dir)
	defaultBranch, haveDefaultBranch := migrationgit.DefaultBranchVersions(repoRoot, dir)
	findings = append(findings, lintVersions(files, mergeBaseMax, haveMergeBase, defaultBranch, haveDefaultBranch)...)
	if len(files) == 0 {
		if len(findings) > 0 {
			return Result{Findings: findings, Dir: dir}, nil
		}
		return Result{Dir: dir, Skipped: fmt.Sprintf("%s exists but holds no *.up.sql files — create one with `forge db migration new <name>`", dir)}, nil
	}

	var adopting []adoptingFile
	var contents []compatFile
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return Result{}, err
		}
		contents = append(contents, compatFile{path: file, content: string(data)})
		findings = append(findings, lintMigrationFile(file, string(data), cfg)...)
		a, adoptFindings := lintAdoption(file, string(data))
		findings = append(findings, adoptFindings...)
		if a != nil {
			adopting = append(adopting, *a)
		}
	}
	findings = append(findings, lintAdoptionPlacement(files, adopting)...)
	findings = append(findings, lintCompatDeclarations(contents, adopting, cfg.RequireCompatDeclaration)...)
	return Result{Findings: findings, Dir: dir, Scanned: files}, nil
}

var (
	statementSplitRe  = regexp.MustCompile(`;`)
	addColumnRe       = regexp.MustCompile(`(?is)\balter\s+table\b[^;]*\badd\s+column\s+(?:if\s+not\s+exists\s+)?(?:"[^"]+"|[a-zA-Z_][\w$]*)\s+[^;]*`)
	setNotNullRe      = regexp.MustCompile(`(?is)\balter\s+table\b[^;]*\balter\s+column\s+(?:"([^"]+)"|([a-zA-Z_][\w$]*))\s+set\s+not\s+null\b`)
	updateRe          = regexp.MustCompile(`(?is)^\s*update\s+(?:"[^"]+"|[a-zA-Z_][\w$.]*)\s+set\s+([^;]+)\bwhere\b[^;]+`)
	columnSetRe       = regexp.MustCompile(`(?is)(?:"([^"]+)"|([a-zA-Z_][\w$]*))\s*=`)
	destructiveRe     = regexp.MustCompile(`(?is)\b(drop\s+table|drop\s+schema|drop\s+column|truncate\s+table|alter\s+column\s+[^;]+\s+type\b|rename\s+column|rename\s+to)\b`)
	volatileDefaultRe = regexp.MustCompile(`(?is)\bdefault\s+(now\s*\(|current_timestamp\b|clock_timestamp\s*\(|statement_timestamp\s*\(|transaction_timestamp\s*\(|gen_random_uuid\s*\(|uuid_generate_v4\s*\(|random\s*\()`)
	lineCommentRe     = regexp.MustCompile(`(?m)--.*$`)
	blockCommentRe    = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func lintMigrationFile(file, content string, cfg RuleConfig) []Finding {
	// Honor in-file pragmas before stripping comments. The destructive-
	// change rule is opted out per migration, in the file, with
	// `-- forge:allow-destructive`: the intent lives next to the SQL it
	// excuses, so it cannot outlive or drift from that file.
	allowDestructive := hasAllowDestructivePragma(content)
	// The NOT NULL rules have their own opt-out, deliberately separate from
	// the destructive one: the two suppress different hazards, and a single
	// pragma would hand every migration that already carries
	// allow-destructive a silent exemption from the NOT NULL checks too.
	allowUnsafeNotNull := hasAllowUnsafeNotNullPragma(content)

	clean := stripSQLComments(content)
	statements := splitStatements(clean)
	backfilledColumns := map[string]bool{}
	// generatedColumns records every column this migration adds as
	// GENERATED ALWAYS AS (...) STORED, so a later SET NOT NULL on one is
	// recognized as safe. It is scoped to the file for the same reason
	// backfilledColumns is: that is the whole extent of what the linter can
	// see without a live schema, and guessing beyond it would be worse than
	// the finding.
	generatedColumns := map[string]bool{}
	var findings []Finding

	for _, stmt := range statements {
		text := strings.TrimSpace(stmt.Text)
		if text == "" {
			continue
		}

		if severity := severityFor(cfg.DestructiveChange); severity != "" && destructiveRe.MatchString(text) && !allowDestructive {
			findings = append(findings, Finding{
				File:     file,
				Line:     stmt.Line,
				Rule:     "destructive-change",
				Severity: severity,
				Message:  "destructive migration operation detected; split data-preserving rollout steps or allowlist this file intentionally",
			})
		}

		for _, column := range updatedColumns(text) {
			backfilledColumns[strings.ToLower(column)] = true
		}
		for _, column := range addedGeneratedStoredColumns(text) {
			generatedColumns[strings.ToLower(column)] = true
		}

		if severity := severityFor(cfg.UnsafeAddColumn); severity != "" && !allowUnsafeNotNull &&
			addColumnRe.MatchString(text) && unsafeAddedNotNull(text) {
			findings = append(findings, Finding{
				File:     file,
				Line:     stmt.Line,
				Rule:     "unsafe-add-not-null-column",
				Severity: severity,
				Message:  "ADD COLUMN ... NOT NULL without a DEFAULT fails on populated tables; add nullable column, backfill, then SET NOT NULL",
			})
		}

		if severity := severityFor(cfg.VolatileDefault); severity != "" && addColumnRe.MatchString(text) && volatileDefaultRe.MatchString(text) {
			findings = append(findings, Finding{
				File:     file,
				Line:     stmt.Line,
				Rule:     "volatile-default",
				Severity: severity,
				Message:  "volatile DEFAULT in ADD COLUMN can rewrite/lock populated tables; prefer nullable column plus explicit backfill",
			})
		}

		if severity := severityFor(cfg.UnsafeAddColumn); severity != "" && !allowUnsafeNotNull {
			for _, column := range setNotNullColumns(text) {
				key := strings.ToLower(column)
				if !backfilledColumns[key] && !generatedColumns[key] {
					findings = append(findings, Finding{
						File:     file,
						Line:     stmt.Line,
						Rule:     "set-not-null-without-backfill",
						Severity: severity,
						Message:  fmt.Sprintf("SET NOT NULL on column %q without an earlier UPDATE backfill in this migration can fail on populated tables", column),
					})
				}
			}
		}
	}
	// The file-as-one-transaction rules read the original content: they
	// lex comments and strings themselves (see lex.go).
	return append(findings, lintTransactionBlock(file, content)...)
}

type statement struct {
	Text string
	Line int
}

// splitStatements splits content on `;` and records the line each
// statement's TEXT starts on — the line a finding should point at.
//
// A part begins right after the previous `;`, so it usually opens with that
// line's newline and any blank or comment lines (comments are already blanked
// to newlines). Counting from the part's first byte put every finding on the
// line of the PREVIOUS statement's semicolon — and a migration's first
// statement, under the header `forge db migration new` writes, on line 1.
func splitStatements(content string) []statement {
	parts := statementSplitRe.Split(content, -1)
	statements := make([]statement, 0, len(parts))
	line := 1
	for _, part := range parts {
		lead := part[:len(part)-len(strings.TrimLeft(part, " \t\r\n"))]
		statements = append(statements, statement{Text: part, Line: line + strings.Count(lead, "\n")})
		line += strings.Count(part, "\n")
	}
	return statements
}

func stripSQLComments(content string) string {
	content = blockCommentRe.ReplaceAllStringFunc(content, func(match string) string {
		return strings.Repeat("\n", strings.Count(match, "\n"))
	})
	return lineCommentRe.ReplaceAllString(content, "")
}

func hasNotNull(statement string) bool {
	return notNullRe.MatchString(statement)
}

// generatedStoredRe matches a GENERATED ALWAYS AS (<expr>) STORED column
// definition.
//
// Deliberately narrow on both ends. `AS (` excludes GENERATED ALWAYS AS
// IDENTITY, which shares the leading keywords but is a sequence-backed add
// with no expression over existing columns — a different operation the
// exemption's reasoning does not cover. Requiring STORED excludes postgres
// 18's VIRTUAL form, which computes on read; only STORED materializes the
// value for every existing row during the ADD COLUMN, which is the entire
// basis for calling the add safe.
//
// [^;]*? between the closing paren and STORED is lazy and stops at the
// statement boundary, so a later statement's STORED cannot be borrowed to
// exempt an earlier plain add.
var (
	notNullRe = regexp.MustCompile(`(?is)\bnot\s+null\b`)
	defaultRe = regexp.MustCompile(`(?is)\bdefault\b`)
	// addGeneratedStoredRe pulls the COLUMN NAME out of such an add, so a
	// SET NOT NULL later in the same file can be matched against it.
	addGeneratedStoredRe = regexp.MustCompile(`(?is)\badd\s+column\s+(?:if\s+not\s+exists\s+)?(?:"([^"]+)"|([a-zA-Z_][\w$]*))\s+[^;]*?\bgenerated\s+always\s+as\s*\([^;]*?\)[^;]*?\bstored\b`)
)

// addedGeneratedStoredColumns reports columns postgres computes and materializes
// itself, for the later SET NOT NULL backfill exemption.
//
// This is the N3 exemption. unsafe-add-not-null-column exists because a plain
// NOT NULL add has no value for the rows already in the table and the
// statement fails outright. A GENERATED ALWAYS ... STORED column has a value
// for every one of them by construction — postgres evaluates the expression
// per row as part of the add — so that failure mode does not apply.
//
// Without the exemption the rule had NO satisfiable spelling: its own
// remediation ("add nullable column, backfill, then SET NOT NULL") is invalid
// SQL against a generated column, since postgres rejects an UPDATE of one, and
// the split form then tripped set-not-null-without-backfill instead. A
// measured dogfood run escaped only by dropping NOT NULL, which projects the
// Go field as a pointer and makes every consumer nil-check a value that
// arithmetic over NOT NULL columns can never produce. A rule with no
// satisfiable spelling is worse than no rule: it buys a weaker schema.
// addedGeneratedStoredColumns returns the columns this statement adds as
// GENERATED ALWAYS AS (...) STORED.
func addedGeneratedStoredColumns(statement string) []string {
	matches := addGeneratedStoredRe.FindAllStringSubmatch(statement, -1)
	columns := make([]string, 0, len(matches))
	for _, match := range matches {
		columns = append(columns, firstNonEmpty(match[1], match[2]))
	}
	return columns
}

func hasDefault(statement string) bool {
	return defaultRe.MatchString(statement)
}

func setNotNullColumns(statement string) []string {
	matches := setNotNullRe.FindAllStringSubmatch(statement, -1)
	columns := make([]string, 0, len(matches))
	for _, match := range matches {
		columns = append(columns, firstNonEmpty(match[1], match[2]))
	}
	return columns
}

func updatedColumns(statement string) []string {
	match := updateRe.FindStringSubmatch(statement)
	if match == nil {
		return nil
	}
	setClause := match[1]
	matches := columnSetRe.FindAllStringSubmatch(setClause, -1)
	columns := make([]string, 0, len(matches))
	for _, m := range matches {
		columns = append(columns, firstNonEmpty(m[1], m[2]))
	}
	return columns
}

func severityFor(value string) Severity {
	// Empty Severity ("") means "rule disabled" — ParseSeverity returns
	// ("", false) for unrecognized values, which collapses to the same
	// disabled sentinel the callers already check for.
	sev, _ := finding.ParseSeverity(value)
	return sev
}

// DestructiveChangeRemediation is the actionable fix text for a
// destructive-change finding. The one suppression mechanism is the in-file
// pragma `-- forge:allow-destructive` (allowDestructivePragmaRe below).
//
// This const is colocated with the pragma regex so the syntax we tell
// users can't drift from the syntax the linter matches.
const DestructiveChangeRemediation = `either rewrite the destructive migration as a non-destructive sequence, or — if the change is intentional — mark the migration file with a "-- forge:allow-destructive" comment`

// allowDestructivePragmaRe matches the in-file opt-out. Whitespace between
// tokens is permissive; the leading `--` must be a SQL line comment.
// Examples that match:
//
//	-- forge:allow-destructive
//	--   FORGE:ALLOW-DESTRUCTIVE
var allowDestructivePragmaRe = regexp.MustCompile(`(?im)^\s*--\s*forge:allow-destructive\b`)

func hasAllowDestructivePragma(content string) bool {
	return allowDestructivePragmaRe.MatchString(content)
}

// AllowDestructiveFiles lists the migration files in dir that carry the
// `-- forge:allow-destructive` directive, by base name, sorted. It is what
// `forge project audit` reports, so the exemptions stay reviewable without a
// forge.yaml list to keep in sync.
func AllowDestructiveFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && hasAllowDestructivePragma(string(body)) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// UnsafeNotNullRemediation is the fix text for the two NOT NULL rules. It
// names the generated-column spelling FIRST because that is the durable fix —
// the column stops being something the application can get wrong — and the
// pragma second, as the acknowledgement for cases the linter cannot see (an
// empty table, or a column made generated in an earlier migration).
//
// Colocated with allowUnsafeNotNullPragmaRe so the syntax we tell users cannot
// drift from the syntax the linter matches.
const UnsafeNotNullRemediation = `add the column nullable, backfill it with an UPDATE, then SET NOT NULL in the same migration; for a derived column prefer making the database compute it (ADD COLUMN ... GENERATED ALWAYS AS (<expr>) STORED NOT NULL, which this rule accepts); or — if you know the table is empty — mark the migration file with a "-- forge:allow-unsafe-not-null" comment`

// allowUnsafeNotNullPragmaRe matches the in-file opt-out for
// unsafe-add-not-null-column and set-not-null-without-backfill, in the same
// two forms the destructive pragma accepts:
//
//	-- forge:allow-unsafe-not-null
//	-- forge-safety: allow-unsafe-not-null
var allowUnsafeNotNullPragmaRe = regexp.MustCompile(`(?im)^\s*--\s*(?:forge:allow-unsafe-not-null\b|forge-safety:\s*allow-unsafe-not-null\b)`)

func hasAllowUnsafeNotNullPragma(content string) bool {
	return allowUnsafeNotNullPragmaRe.MatchString(content)
}

// Allowance is one in-file migration-safety pragma: the rules it silences for
// the whole migration, where it is written, and any text after it on its line.
// It is what `forge lint --show-suppressed` lists, read with the same regexes
// the rules honour so the two cannot drift.
type Allowance struct {
	Pragma string
	Rules  []string
	Line   int
	Reason string
}

// Allowances lists every safety pragma in one migration's content, in source
// order.
func Allowances(content string) []Allowance {
	var out []Allowance
	for _, p := range []struct {
		pragma string
		re     *regexp.Regexp
		rules  []string
	}{
		{"forge:allow-destructive", allowDestructivePragmaRe, []string{"destructive-change"}},
		{"forge:allow-unsafe-not-null", allowUnsafeNotNullPragmaRe, []string{"unsafe-add-not-null-column", "set-not-null-without-backfill"}},
		{"forge:allow-in-transaction", allowInTransactionPragmaRe, []string{RuleNonTransactionalStatement, RuleEnumValueUsedBeforeCommit}},
	} {
		for _, loc := range p.re.FindAllStringIndex(content, -1) {
			// The regex's leading \s* can span blank lines; the pragma's
			// line is the one its `--` is on.
			start := loc[0] + strings.Index(content[loc[0]:loc[1]], "--")
			rest := content[loc[1]:]
			if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
				rest = rest[:nl]
			}
			out = append(out, Allowance{
				Pragma: p.pragma,
				Rules:  p.rules,
				Line:   strings.Count(content[:start], "\n") + 1,
				Reason: strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(rest), ":—–-")),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// RemediationFor returns the fix text for a rule ID.
//
// Every migration-safety failure used to be printed with the DESTRUCTIVE
// remediation regardless of which rule fired, so an author whose NOT NULL add
// was rejected was told to add `-- forge:allow-destructive`, which does not
// silence it. Advice that does not apply to the finding it is attached to is
// how an author ends up chasing a hatch that does nothing.
//
// An unknown rule falls back to the destructive text, which is the historical
// behaviour and the right default for the rule that has no other hatch.
func RemediationFor(rule string) string {
	switch rule {
	case "unsafe-add-not-null-column", "set-not-null-without-backfill":
		return UnsafeNotNullRemediation
	case "volatile-default":
		return VolatileDefaultRemediation
	case RuleNoDownMigration:
		return NoDownMigrationRemediation
	case RuleDuplicateVersion:
		return DuplicateVersionRemediation
	case RuleNonTimestampVersion:
		return NonTimestampVersionRemediation
	case RuleVersionBelowMergedHead:
		return VersionBelowMergedHeadRemediation
	case RuleInvalidAdoption:
		return InvalidAdoptionRemediation
	case RuleNonTransactionalStatement:
		return NonTransactionalStatementRemediation
	case RuleEnumValueUsedBeforeCommit:
		return EnumValueUsedBeforeCommitRemediation
	case RuleCompatUndeclared:
		return CompatUndeclaredRemediation
	case RuleCompatReason:
		return CompatReasonRemediation
	case RuleCompatConflict:
		return CompatConflictRemediation
	default:
		return DestructiveChangeRemediation
	}
}

// VolatileDefaultRemediation is the fix text for a volatile-default finding.
const VolatileDefaultRemediation = `add the column nullable, backfill it with an explicit UPDATE, then set the default for new rows — a volatile DEFAULT is evaluated per existing row and rewrites the whole table under an ACCESS EXCLUSIVE lock`

// PrimaryRemediation returns the fix text for a set of findings: the rule's
// own text when they all share one rule, else the generic pointer at the
// per-finding hints. The CLI prints ONE fix line under a batch of findings,
// and picking any single rule's text for a mixed batch is how the wrong
// advice got attached in the first place.
func PrimaryRemediation(findings []Finding) string {
	rules := map[string]bool{}
	rule := ""
	for _, f := range findings {
		if f.Severity != SeverityError {
			continue
		}
		rules[f.Rule] = true
		if rule == "" {
			rule = f.Rule
		}
	}
	if len(rules) > 1 {
		return mixedRemediation(rules)
	}
	if rule == "" {
		return DestructiveChangeRemediation
	}
	return RemediationFor(rule)
}

// MixedRemediation leads what a batch spanning several rules gets: the fix
// depends on which finding you are looking at, so say that rather than print
// one rule's hatch over all of them. mixedRemediation adds the hatches of
// the rules actually present — and only those: naming
// `-- forge:allow-destructive` under a batch of compatibility findings sends
// the author after a hatch that silences none of them.
const MixedRemediation = `each finding above names the rule it violated; run with --json for the per-finding fix`

// ruleHatch is the one-line way to settle a rule in a mixed batch's closing
// advice, for the rules that have one.
var ruleHatch = map[string]string{
	"destructive-change":            `destructive-change accepts "-- forge:allow-destructive"`,
	"unsafe-add-not-null-column":    `the NOT NULL rules accept "-- forge:allow-unsafe-not-null"`,
	"set-not-null-without-backfill": `the NOT NULL rules accept "-- forge:allow-unsafe-not-null"`,
	RuleCompatUndeclared:            `the compatibility rules take "-- forge:backward-compatible — <why>" or "-- forge:breaks-rollback <why>"`,
	RuleCompatReason:                `the compatibility rules take "-- forge:backward-compatible — <why>" or "-- forge:breaks-rollback <why>"`,
	RuleCompatConflict:              `the compatibility rules take "-- forge:backward-compatible — <why>" or "-- forge:breaks-rollback <why>"`,
}

// mixedRemediation is MixedRemediation plus the hatches of the rules in
// rules, each once, in a stable order.
func mixedRemediation(rules map[string]bool) string {
	seen := map[string]bool{}
	var hatches []string
	for _, rule := range sortedKeys(rules) {
		if h, ok := ruleHatch[rule]; ok && !seen[h] {
			seen[h] = true
			hatches = append(hatches, h)
		}
	}
	if len(hatches) == 0 {
		return MixedRemediation
	}
	return MixedRemediation + "; " + strings.Join(hatches, "; ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CompatUndeclaredRemediation, CompatReasonRemediation and
// CompatConflictRemediation are the fix text for the compatibility-
// declaration rules (compat_declaration.go). None of them is settled by
// rewriting the SQL or by `-- forge:allow-destructive`.
const (
	CompatUndeclaredRemediation = `declare whether the previous release keeps working on the schema this migration leaves: "-- forge:backward-compatible — <why>" if it does, "-- forge:breaks-rollback <why>" if it deliberately does not ('forge db migration new --backward-compatible <why>' / '--breaks-rollback <why>' writes the line)`
	CompatReasonRemediation     = `write the reason on the declaration's own line: why the previous release keeps working on this schema, or — for "-- forge:breaks-rollback" — why it deliberately does not`
	CompatConflictRemediation   = `keep one declaration: "-- forge:backward-compatible — <why>" if the previous release keeps working on this schema, "-- forge:breaks-rollback <why>" if it does not`
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
