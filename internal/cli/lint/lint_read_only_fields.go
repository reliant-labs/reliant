// File: internal/cli/lint/lint_read_only_fields.go
//
// read-only-fields — `forge lint --read-only-fields`.
//
// The twin of forgeconv-computed-field-unwritten, for the marker where the
// same failure is SILENT.
//
// `// forge:read-only` says a field is not CLIENT-writable, and its only
// mechanical effect is a wire-shape omission: the field is stripped from
// the born Create and Update requests. It says nothing about who writes the
// column instead, and forge — correctly — writes nothing. So a read-only
// column that no app code populates takes its column DEFAULT: 0 for the
// money columns this happens to most (`total_cents`, `balance_cents`,
// `subtotal_cents`), NULL forever for a nullable timestamp, "" for a text
// column. The finding's consequence clause is derived from the column's
// actual type — see lint_unwritten_outcome.go for why a fixed money
// illustration was a trap rather than a shortcut.
//
// Nothing catches it, and that is the whole point of the rule:
//
//   - No constraint is violated. 0 is a perfectly legal BIGINT.
//   - No test fails. The generated CRUD lifecycle test derives its fixtures
//     from the same schema, so it round-trips the zero it just stored and
//     agrees with the defect by construction.
//   - No error is logged, anywhere.
//
// The single symptom is a human reading a screen that says $0.00. In the
// audited run this was found only because the workflow made the agent read
// every .up.sql and ask "what writes this?" — luck of process. Had it not,
// the app would have shipped with every estimate total and every invoice
// balance at zero.
//
// ── Why this is not just the computed rule with a wider marker set ────────
//
// forge:computed declares an OBLIGATION, so its check can be blunt: the
// author said something derives this, and nothing does. forge:read-only
// declares no such thing — many read-only columns are legitimately
// populated by a DEFAULT that means something, by postgres itself, or by
// forge's own managed-timestamp machinery. Applied bluntly to read-only,
// the same check fires on `created_at` for every entity in the project,
// which is precisely how a lint earns a blanket disable and stops
// protecting the one case that mattered.
//
// So the schema is consulted, and a finding requires ALL of:
//
//  1. the field carries `forge:read-only` (NOT `forge:computed` — that one
//     is forgeconv-computed-field-unwritten's, and reporting one defect
//     under two rule ids with two fix hints teaches authors to ignore both);
//  2. the column's DEFAULT is absent or is the type's zero value — a real
//     default like `'draft'` or `5` is evidence somebody chose the value;
//  3. the column is NOT `GENERATED ALWAYS AS (...) STORED` — postgres
//     writes those on every insert and update, and it is the very fix this
//     rule recommends;
//  4. the column is not NOT NULL-with-no-DEFAULT — codegen's
//     FindUnsatisfiableColumns already FAILS `forge generate` on exactly
//     that shape, and a warning beside a hard error reads like the softer
//     of two verdicts;
//  5. the column declares no `forge:fill=` strategy — that marker already
//     answers the question this rule asks;
//  6. the column is not one of forge's managed columns (created_at,
//     updated_at, deleted_at), which pkg/crud stamps from generated code
//     the Go scan deliberately cannot see;
//  7. no non-generated, non-test Go file assigns the Go field name, AND no
//     SQL assigns the COLUMN name — a trigger body's `NEW.col := …` or an
//     `UPDATE … SET col = …`, in a migration or in a Go string literal.
//     Those are real write paths the AST scan structurally cannot see,
//     because none of them spells the protoc-gen-go field identifier it
//     searches for. See columnsWrittenBySQL.
//
// ── Why the schema is read as TEXT ────────────────────────────────────────
//
// schemadef.ApplyAndIntrospect is the authoritative answer, and it needs a
// real postgres to apply the migrations to. A lint that requires a database
// cannot run in the half-finished state where this defect actually lives,
// and `forge lint` has no shadow server of its own. So DEFAULT / GENERATED
// / NOT NULL are parsed out of db/migrations the same way the sibling
// crud-fixtures check reads foreign keys, replaying files in lexical order
// so a later ALTER wins.
//
// The trade is that a column this parser cannot resolve yields SILENCE, not
// a finding — see readOnlyColumnsFromMigrations. That direction is chosen
// deliberately: a rule that misses a case costs one defect, while a rule
// that invents one costs every future finding it would have reported.
//
// ── Severity: this rule GATES ─────────────────────────────────────────────
//
// It was warnings-only, matching its computed-field twin, and that was
// backwards relative to the argument above. The whole case for the check is
// that the defect "ships as $0.00 with no error, no failing test, and no log
// line anywhere" — and a warning inside a hundred-line lint run is very
// close to no log line anywhere. The audited project caught its two only
// because someone was deliberately looking for them.
//
// Its computed-field twin gates too, and at least as hard: forge:computed is
// the stronger promise ("my app derives this"), so the old asymmetry — read-
// only failing while computed only warned — was backwards. Both rules share
// one exception, and it is deterministic rather than a judgement call:
// while the declaring service's handler package still carries forge's own
// `forge:gen unwired-stub` placeholders, the finding is a warning naming
// them ("pending: implement ChangeJobStatus"), because the rpc that will
// write the column is still forge's fresh output. See lint_pending_stubs.go.
//
// A gating rule that fires wrongly is far worse than a warning that does,
// so every exclusion above is pinned by a test, and the two write paths
// this check could not previously see were closed BEFORE the verdict
// changed (see lint_read_only_fields_gating_test.go).
//
// The escape hatch is `COMMENT ON COLUMN <table>.<col> IS
// 'forge:fill=handler'`, and it is deliberately the only one. A severity
// dial would let a project silence the rule without answering its question;
// the marker makes the author state WHO populates the column, which is a
// fact the next reader needs and the answer the rule was asking for. Note
// that `lint.rules` does NOT reach this check — those severities are
// applied to finding.Finding values, and this step emits its own report —
// so the marker is not merely preferred, it is the mechanism.

package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// readOnlyFieldFinding is one `forge:read-only` field whose column nothing
// populates. File/Line point at the PROTO declaration — where the marker
// was written, and where the author decides between deriving the value and
// making postgres derive it.
type readOnlyFieldFinding struct {
	File   string
	Line   int
	Entity string
	Field  string
	// GoField is the protoc-gen-go spelling searched for ("TotalCents"),
	// carried so the fix hint can name the identifier the author must
	// actually assign rather than the proto spelling, which appears
	// nowhere in Go.
	GoField string
	// Table is the resolved table name, and Default the column's DEFAULT
	// expression verbatim ("" when it has none). Both are reported because
	// the fix is a migration against that column, and naming the default
	// is what makes "ships as $0.00" a statement about this schema rather
	// than a generic warning.
	Table   string
	Default string
	// SQLType is the column's declared type ("BIGINT", "TIMESTAMPTZ"),
	// carried so the consequence clause can describe what THIS column
	// ships as. A fixed money illustration spliced into a timestamp
	// finding reads as a type mis-detection — see lint_unwritten_outcome.go.
	SQLType string
	// Pending holds the finding at warning while the declaring service
	// still carries forge-scaffolded unwired stubs (lint_pending_stubs.go).
	Pending pendingStubs
}

// readOnlyFieldFixHint renders the remediation. GENERATED ALWAYS AS is
// named FIRST because it is the durable answer for a derived column — the
// database computes it on every write, so no code path can forget to — and
// it is what the audited project ultimately used.
//
// The recommended spelling carries NOT NULL on purpose. A generated column
// over NOT NULL inputs can never yield NULL, and a nullable one projects the
// Go field as a pointer, so every consumer nil-checks a value that cannot be
// nil. This advice previously stopped at STORED, and following it then tripped
// `--migration-safety`'s unsafe-add-not-null-column, which had no satisfiable
// spelling for a generated column — the audited run escaped by dropping NOT
// NULL and paid exactly that pointer cost. migrationlint now exempts
// GENERATED ALWAYS ... STORED, so the two rules agree; keep them that way.
func readOnlyFieldFixHint(f readOnlyFieldFinding) string {
	shipped := "the column default"
	if d := strings.TrimSpace(f.Default); d != "" {
		shipped = fmt.Sprintf("the column default (%s)", d)
	}
	return fmt.Sprintf(
		"%s.%s is marked `%s` but no non-generated Go file assigns %s, and %s.%s has no "+
			"DEFAULT that populates it. The field is omitted from Create/Update, so nothing "+
			"writes the column and every row takes %s — %s, with no error, no failing test, "+
			"and no log line anywhere. Prefer making the "+
			"database compute it: `ALTER TABLE %s ADD COLUMN %s <type> GENERATED ALWAYS AS "+
			"(<expression>) STORED NOT NULL` cannot be forgotten by any write path. If the value derives "+
			"from OTHER ROWS (postgres cannot reach another table from a generated column), "+
			"derive it in Go and mark the field `%s` so `forge lint --computed-fields` holds "+
			"you to it. If something this check cannot see already writes it — a trigger, "+
			"another service — declare that with `COMMENT ON COLUMN %s.%s IS '%s=handler'`.",
		f.Entity, f.Field, codegen.ProtoMarkerReadOnly, f.GoField, f.Table, f.Field,
		shipped, shapeConsequence(sqlValueShape(f.SQLType, f.Field)),
		f.Table, f.Field, codegen.ProtoMarkerComputed,
		f.Table, f.Field, schemadef.ColumnMarkerFill)
}

// readOnlyFieldRuleID is the rule id both report formats use.
const readOnlyFieldRuleID = "forgeconv-read-only-field-unwritten"

// collectReadOnlyFieldFindings is the shared engine behind text mode and
// `forge lint --json`. A project with no proto tree, or no migrations,
// yields nothing: without both halves there is no correlation to make, and
// guessing from one of them is the false positive this rule cannot afford.
func collectReadOnlyFieldFindings(projectDir, migrationsDir string) ([]readOnlyFieldFinding, error) {
	protoRoot := filepath.Join(projectDir, protoDirDefault)
	if _, err := os.Stat(protoRoot); os.IsNotExist(err) {
		return nil, nil
	}
	if !filepath.IsAbs(migrationsDir) {
		migrationsDir = filepath.Join(projectDir, migrationsDir)
	}
	columns, err := readOnlyColumnsFromMigrations(migrationsDir)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, nil
	}
	dirs, err := protoSubdirsWithFiles(protoRoot)
	if err != nil {
		return nil, err
	}

	// Collect the candidate fields first, so the (more expensive) Go scan
	// is skipped entirely when the schema already explains every one of
	// them — the common case for a project whose read-only columns are
	// GENERATED or managed.
	type candidate struct {
		entity, field, goField, file, table, def, sqlType string
		line                                              int
		pending                                           pendingStubs
	}
	var candidates []candidate
	for _, dir := range dirs {
		scan, scanErr := codegen.ScanRawProtoDir(dir)
		if scanErr != nil {
			continue // buf lint / generate report a malformed proto far better
		}
		var pending pendingStubs
		resolved := false
		for _, msg := range scan.Messages {
			table, cols, ok := tableForEntity(columns, msg.Name)
			if !ok {
				// The schema half is a text parse, so an entity whose
				// table it did not resolve is a gap in THIS parser, not
				// evidence about the app. Silence is the only sound
				// answer; see the file header.
				continue
			}
			for _, name := range plainReadOnlyFieldNames(msg) {
				col, known := cols[name]
				if !known || !columnIsUnpopulated(col) {
					continue
				}
				// Once per directory, and only when a candidate needs it.
				if !resolved {
					pending = pendingStubsForScan(projectDir, scan)
					resolved = true
				}
				candidates = append(candidates, candidate{
					entity:  msg.Name,
					field:   name,
					goField: naming.GoCamelCase(name),
					file:    msg.File,
					line:    fieldLineIn(msg, name),
					table:   table,
					def:     col.Default,
					sqlType: col.Type,
					pending: pending,
				})
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	written, err := assignedGoFields(projectDir)
	if err != nil {
		return nil, err
	}
	// The SQL half of "what writes this column". A trigger body and a
	// column-named UPDATE are both write paths the Go scan structurally
	// cannot see — see columnsWrittenBySQL.
	sqlWritten, err := columnsWrittenBySQL(migrationsDir)
	if err != nil {
		return nil, err
	}
	goSQLWritten, err := columnsWrittenByGoSQLLiterals(projectDir)
	if err != nil {
		return nil, err
	}
	for col := range goSQLWritten {
		sqlWritten[col] = true
	}

	var findings []readOnlyFieldFinding
	for _, c := range candidates {
		if fieldWritten(written, c.field) || sqlWritten[c.field] {
			continue
		}
		findings = append(findings, readOnlyFieldFinding{
			File: relToProject(projectDir, c.file), Line: c.line,
			Entity: c.entity, Field: c.field, GoField: c.goField,
			Table: c.table, Default: c.def, SQLType: c.sqlType,
			Pending: c.pending,
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

// tableForEntity resolves the table an entity message maps to, trying the
// pluralized snake_case name forge generates and then the singular — the
// same two spellings internal/scaffold considers. An entity whose table is
// neither is reported as unresolved rather than guessed at.
func tableForEntity(columns map[string]map[string]sqlColumn, entity string) (string, map[string]sqlColumn, bool) {
	snake := naming.ToSnakeCase(entity)
	for _, candidate := range []string{naming.Pluralize(snake), snake} {
		if cols, ok := columns[candidate]; ok {
			return candidate, cols, true
		}
	}
	return "", nil, false
}

// columnIsUnpopulated reports whether nothing in the SCHEMA writes col —
// the half of the finding that can be answered without reading Go. Each
// arm is one of the exclusions in the file header, and each is pinned by
// its own test.
func columnIsUnpopulated(col sqlColumn) bool {
	switch {
	case col.Generated:
		// Postgres computes it on every write. This is the fix the rule
		// recommends; firing on it would tell the author to undo it.
		return false
	case col.PrimaryKey:
		// Written by the insert, by forge:fill=ulid, or by the client.
		return false
	case col.FillDeclared:
		// `forge:fill=` already names who populates the column.
		return false
	case isManagedColumn(col.Name):
		// pkg/crud stamps created_at/updated_at and manages deleted_at,
		// all from GENERATED code the Go scan excludes by design. Without
		// this arm the rule fires on every entity in the project at once.
		return false
	case strings.TrimSpace(col.Default) == "" && col.NotNull:
		// NOT NULL with no DEFAULT is codegen.FindUnsatisfiableColumns's,
		// and that one GATES `forge generate`. Reporting it as a warning
		// too would read like the softer of two verdicts for one defect.
		return false
	case strings.TrimSpace(col.Default) == "":
		// Nullable with no default: every row is NULL, and nothing says so.
		return true
	default:
		return isZeroValueDefault(col.Default)
	}
}

// isManagedColumn names the columns forge's own machinery populates.
func isManagedColumn(name string) bool {
	switch name {
	case schemadef.ColCreatedAt, schemadef.ColUpdatedAt, schemadef.ColDeletedAt:
		return true
	}
	return false
}

// zeroValueDefaultRE matches a DEFAULT expression that stores the type's
// zero value — the defaults that are indistinguishable from "nobody chose
// anything". A default outside this set (`'draft'`, `5`, `now()`) is
// evidence of intent and takes the column out of scope.
//
// Deliberately literal-only: a function call could return anything, and
// `now()` in particular IS a population. Anything unrecognized reads as a
// meaningful default, which keeps the unknown case silent.
var zeroValueDefaultRE = regexp.MustCompile(`^(?:0+(?:\.0+)?|''|'0'|false|'\{\}'|'\[\]'|'{}'::jsonb|'\[\]'::jsonb)$`)

// isZeroValueDefault reports whether def stores the type's zero value,
// after stripping the wrapping parens and `::type` cast postgres echoes
// back (`(0)::bigint`).
func isZeroValueDefault(def string) bool {
	d := strings.ToLower(strings.TrimSpace(def))
	if i := strings.Index(d, "::"); i >= 0 {
		// Keep the jsonb spellings whole — they are zero values in their
		// own right, not casts of one.
		if !strings.HasPrefix(d, "'{}'") && !strings.HasPrefix(d, "'[]'") {
			d = strings.TrimSpace(d[:i])
		}
	}
	for strings.HasPrefix(d, "(") && strings.HasSuffix(d, ")") {
		d = strings.TrimSpace(d[1 : len(d)-1])
	}
	return zeroValueDefaultRE.MatchString(d)
}

// plainReadOnlyFieldNames returns the fields of msg carrying
// `forge:read-only` and NOT `forge:computed`.
//
// The raw scan records both markers as ReadOnly and keeps no separate bit
// (ReadOnlyProtoMarkers is deliberately one set), so membership is re-read
// from the source text here — the same call the computed rule makes, for
// the same reason: the two markers are identical everywhere EXCEPT which
// check holds the author to what.
func plainReadOnlyFieldNames(msg codegen.RawProtoMessage) []string {
	data, err := os.ReadFile(msg.File)
	if err != nil {
		return nil
	}
	content := string(data)
	if msg.BodyOpen < 0 || msg.BodyClose > len(content) || msg.BodyOpen >= msg.BodyClose {
		return nil
	}
	declared := make(map[string]bool, len(msg.Fields))
	for _, f := range msg.Fields {
		declared[f.Name] = true
	}

	readOnlyRE := codegen.ProtoMarkerAnyLineRE([]string{codegen.ProtoMarkerReadOnly})
	computedRE := codegen.ProtoMarkerAnyLineRE([]string{codegen.ProtoMarkerComputed})
	var out []string
	// Both accepted marker positions, matching the scanner's own rule: a
	// trailing comment binds to the field on that line, a full-line one
	// binds to the next field declared.
	pendingReadOnly, pendingComputed := false, false
	for _, line := range strings.Split(content[msg.BodyOpen:msg.BodyClose], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // blank line — a pending marker survives
		}
		if strings.HasPrefix(trimmed, "//") {
			if readOnlyRE.MatchString(trimmed) {
				pendingReadOnly = true
			}
			if computedRE.MatchString(trimmed) {
				pendingComputed = true
			}
			continue
		}
		name, ok := protoFieldNameOnLine(line)
		if !ok || !declared[name] {
			continue
		}
		readOnly := pendingReadOnly || readOnlyRE.MatchString(line)
		computed := pendingComputed || computedRE.MatchString(line)
		if readOnly && !computed {
			out = append(out, name)
		}
		pendingReadOnly, pendingComputed = false, false
	}
	return out
}

// ── Columns out of migration text ─────────────────────────────────────────

// sqlColumn is what this check needs to know about one column. It is a
// TEXT reading of the migrations, not an introspection — see the file
// header for why, and for the silence-over-guessing rule that follows.
type sqlColumn struct {
	Name string
	// Type is the declared type as written, minus the constraint tail
	// ("BIGINT", "NUMERIC(12, 2)", "TIMESTAMP WITH TIME ZONE"). It exists
	// only so a finding's consequence clause can be true of this column
	// rather than of the bug class in general.
	Type       string
	NotNull    bool
	Default    string
	Generated  bool
	PrimaryKey bool
	// FillDeclared records a `forge:fill=` COMMENT ON COLUMN, which names
	// who populates the column and so answers this rule's question.
	FillDeclared bool
}

var (
	alterAddColumnRe = regexp.MustCompile(
		`(?is)\bALTER\s+TABLE\s+(?:ONLY\s+)?(` + sqlident.Qualified + `)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([^;]+)`)
	alterDropColumnRe = regexp.MustCompile(
		`(?is)\bALTER\s+TABLE\s+(?:ONLY\s+)?(` + sqlident.Qualified + `)\s+DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(` + sqlident.Part + `)`)
	alterSetDefaultRe = regexp.MustCompile(
		`(?is)\bALTER\s+TABLE\s+(?:ONLY\s+)?(` + sqlident.Qualified + `)\s+ALTER\s+(?:COLUMN\s+)?(` + sqlident.Part + `)\s+SET\s+DEFAULT\s+([^;]+)`)
	alterDropDefaultRe = regexp.MustCompile(
		`(?is)\bALTER\s+TABLE\s+(?:ONLY\s+)?(` + sqlident.Qualified + `)\s+ALTER\s+(?:COLUMN\s+)?(` + sqlident.Part + `)\s+DROP\s+DEFAULT`)
	columnDefaultRe = regexp.MustCompile(`(?is)\bDEFAULT\s+(.+?)(?:\s+(?:NOT\s+NULL|NULL|PRIMARY\s+KEY|UNIQUE|REFERENCES|CHECK|GENERATED|COLLATE)\b|$)`)
	generatedColRe  = regexp.MustCompile(`(?is)\bGENERATED\s+ALWAYS\s+AS\s*\(`)
	notNullColRe    = regexp.MustCompile(`(?is)\bNOT\s+NULL\b`)
	primaryKeyColRe = regexp.MustCompile(`(?is)\bPRIMARY\s+KEY\b`)
	fillMarkerRe    = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(schemadef.ColumnMarkerFill) + `\s*=`)
)

// readOnlyColumnsFromMigrations reads the project's columns out of its
// migrations, indexed as columns[table][column].
//
// Migrations are replayed in lexical order — the order the migrator
// applies them — so a later ADD COLUMN, DROP COLUMN, SET DEFAULT or DROP
// TABLE wins over what an earlier file declared, and the result describes
// the schema as it stands after the last migration rather than every
// column the history ever mentioned.
func readOnlyColumnsFromMigrations(migrationsDir string) (map[string]map[string]sqlColumn, error) {
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	if err := filepath.WalkDir(migrationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".up.sql") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", migrationsDir, err)
	}
	sort.Strings(files)

	live := map[string]map[string]sqlColumn{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		// Comments are blanked for the same reason crud-fixtures blanks
		// them: forge's own birth migration writes suggestions it cannot
		// yet apply as commented-out SQL, and reading those would build a
		// schema that does not exist. The fill-marker pass runs on the
		// RAW text, since COMMENT ON COLUMN is a statement, not a comment.
		applyMigrationColumns(live, blankSQLComments(string(data)))
		applyFillMarkers(live, string(data))
	}
	return live, nil
}

// applyMigrationColumns folds one migration's column additions, removals
// and default changes into the running schema.
//
// Statements are applied in SOURCE ORDER, not grouped by kind. That matters
// for the one shape this parser would otherwise get exactly backwards:
//
//	ALTER TABLE estimates DROP COLUMN total_cents;
//	ALTER TABLE estimates ADD COLUMN total_cents BIGINT
//	    GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED;
//
// Drop-then-recreate in one file is the ordinary way to make an existing
// column generated, since postgres has no ALTER for it. Applying every ADD
// before every DROP deletes the column outright, and the schema this
// function returns then claims a column the database actually has does not
// exist — which reads to every caller as "unresolved", i.e. silence, on the
// exact migration most likely to matter.
func applyMigrationColumns(live map[string]map[string]sqlColumn, content string) {
	set := func(table string, col sqlColumn) {
		if live[table] == nil {
			live[table] = map[string]sqlColumn{}
		}
		live[table][col.Name] = col
	}

	// Each statement is collected with the offset it appears at, then the
	// whole set is replayed in that order.
	type stmt struct {
		at    int
		apply func()
	}
	var stmts []stmt
	collect := func(re *regexp.Regexp, build func(groups []string) func()) {
		for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
			groups := make([]string, len(m)/2)
			for g := range groups {
				if m[2*g] >= 0 {
					groups[g] = content[m[2*g]:m[2*g+1]]
				}
			}
			if fn := build(groups); fn != nil {
				stmts = append(stmts, stmt{at: m[0], apply: fn})
			}
		}
	}

	for _, m := range createTableRe.FindAllStringSubmatchIndex(content, -1) {
		table := normIdent(content[m[2]:m[3]])
		open := m[1] - 1 // the '(' the pattern ends on
		end, ok := matchParen(content, open)
		if !ok {
			continue
		}
		body := content[open+1 : end]
		at := m[0]
		stmts = append(stmts, stmt{at: at, apply: func() {
			for _, part := range splitTopLevel(body) {
				if col, ok := parseColumnDef(part); ok {
					set(table, col)
				}
			}
		}})
	}

	collect(alterAddColumnRe, func(g []string) func() {
		col, ok := parseColumnDef(g[2])
		if !ok {
			return nil
		}
		table := normIdent(g[1])
		return func() { set(table, col) }
	})
	collect(alterSetDefaultRe, func(g []string) func() {
		table, name, def := normIdent(g[1]), normIdent(g[2]), strings.TrimSpace(g[3])
		return func() {
			if col, ok := live[table][name]; ok {
				col.Default = def
				set(table, col)
			}
		}
	})
	collect(alterDropDefaultRe, func(g []string) func() {
		table, name := normIdent(g[1]), normIdent(g[2])
		return func() {
			if col, ok := live[table][name]; ok {
				col.Default = ""
				set(table, col)
			}
		}
	})
	collect(alterDropColumnRe, func(g []string) func() {
		table, name := normIdent(g[1]), normIdent(g[2])
		return func() { delete(live[table], name) }
	})
	collect(dropTableRe, func(g []string) func() {
		table := normIdent(g[1])
		return func() { delete(live, table) }
	})

	sort.SliceStable(stmts, func(i, j int) bool { return stmts[i].at < stmts[j].at })
	for _, s := range stmts {
		s.apply()
	}
}

// sqlWrittenColumnRE matches an assignment to a column inside SQL: a
// trigger body's `NEW.total_cents := ...` or `NEW.total_cents = ...`, and an
// `UPDATE ... SET approved_at = ...` clause. Both are real write paths the
// Go scan cannot see, because neither names the protoc-gen-go field
// identifier it searches for.
//
// Deliberately loose about WHICH table the write targets. Resolving that
// would mean parsing the enclosing statement, and this check is a text
// reading of SQL, not a parser (see the file header). The loose match can
// only SUPPRESS a finding, never invent one — which is the direction a rule
// that GATES the build has to fail in.
var sqlWrittenColumnRE = regexp.MustCompile(
	`(?i)(?:\bNEW\s*\.\s*("?\w+"?)\s*:?=|\bSET\s+("?\w+"?)\s*=|,\s*("?\w+"?)\s*=)`)

// sqlAssignmentInLiteralRE matches a column assignment at the head of a SQL
// fragment, which is the shape an ORM column-set call carries:
// `Set("approved_at = now()")`, `UpdateColumn("total_cents = ?")`. The SET
// keyword is absent because the ORM supplies it, so the patterns above do
// not reach these.
//
// `=` but not `==`, so a comparison in a WHERE fragment is not read as a
// write.
var sqlAssignmentInLiteralRE = regexp.MustCompile(`^\s*("?\w+"?)\s*=[^=]`)

// columnsWrittenBySQL returns the column names some statement in the
// migrations assigns — a trigger function body, or an UPDATE's SET clause.
//
// This closes the rule's one measured false positive. A column populated by
// a `BEFORE INSERT` trigger, or by a partial UPDATE that names the column
// rather than loading the row, is written by code the project genuinely
// has — it is simply not spelled as a Go struct-field assignment. Reporting
// it as unpopulated contradicts evidence this check already read, and as a
// GATING verdict it would fail the build on correct code.
//
// Returned as a flat column-name set rather than table-qualified, matching
// assignedGoFields next door: both over-suppress across same-named columns,
// and both do so in the direction that cannot manufacture a finding.
func columnsWrittenBySQL(migrationsDir string) (map[string]bool, error) {
	written := map[string]bool{}
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		return written, nil
	}
	err := filepath.WalkDir(migrationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".up.sql") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		// Comments are blanked for the same reason the column scan blanks
		// them: forge's birth migration writes suggestions it cannot yet
		// apply as commented-out SQL, and a commented UPDATE is not a write.
		for _, m := range sqlWrittenColumnRE.FindAllStringSubmatch(blankSQLComments(string(data)), -1) {
			for _, g := range m[1:] {
				if g != "" {
					written[normIdent(g)] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", migrationsDir, err)
	}
	return written, nil
}

// columnsWrittenByGoSQLLiterals returns the column names assigned inside a
// SQL string literal in the project's non-generated, non-test Go source.
//
// This is the second half of the same false positive columnsWrittenBySQL
// closes. `db.NewUpdate().Set("approved_at = now()")` is the ordinary way
// to touch one column without loading the row first, and it is a write the
// AST's selector scan structurally cannot see: no Go field identifier
// appears anywhere in it. A raw `UPDATE ... SET ...` in a string is the
// same case.
//
// Reads the literals from the same AST walk assignedGoFields uses, so the
// two agree exactly on which files count (generated and _test.go excluded,
// gen/ and vendor/ skipped).
func columnsWrittenByGoSQLLiterals(projectDir string) (map[string]bool, error) {
	written := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(projectDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipGoScanDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, "_gen.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil //nolint:nilerr // the Go toolchain reports parse errors far better
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			if m := sqlAssignmentInLiteralRE.FindStringSubmatch(text); m != nil {
				written[normIdent(m[1])] = true
			}
			for _, m := range sqlWrittenColumnRE.FindAllStringSubmatch(text, -1) {
				for _, g := range m[1:] {
					if g != "" {
						written[normIdent(g)] = true
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", projectDir, err)
	}
	return written, nil
}

// applyFillMarkers records which columns declare a `forge:fill=` strategy,
// read from COMMENT ON COLUMN the same way the column-markers lint reads
// them.
func applyFillMarkers(live map[string]map[string]sqlColumn, content string) {
	for _, m := range commentOnColumnRe.FindAllStringSubmatch(content, -1) {
		if !fillMarkerRe.MatchString(m[2]) {
			continue
		}
		// `table.column`, or `schema.table.column` when qualified.
		parts := sqlident.Parts(m[1])
		if len(parts) < 2 {
			continue
		}
		table, name := parts[len(parts)-2], parts[len(parts)-1]
		col, ok := live[table][name]
		if !ok {
			continue
		}
		col.FillDeclared = true
		live[table][name] = col
	}
}

// parseColumnDef reads one CREATE TABLE body part or ADD COLUMN clause as a
// column definition, or ok=false when the part is a table-level constraint
// rather than a column.
func parseColumnDef(part string) (sqlColumn, bool) {
	trimmed := strings.TrimSpace(part)
	if trimmed == "" {
		return sqlColumn{}, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return sqlColumn{}, false
	}
	name := normIdent(fields[0])
	// `PRIMARY KEY (a, b)`, `CHECK (...)`, `FOREIGN KEY ...` — a
	// constraint clause is not a column, and reading one as a column named
	// PRIMARY would put a phantom in the schema.
	if isConstraintKeyword(name) {
		return sqlColumn{}, false
	}
	col := sqlColumn{
		Name:       name,
		Type:       columnTypeIn(trimmed, fields),
		NotNull:    notNullColRe.MatchString(trimmed),
		Generated:  generatedColRe.MatchString(trimmed),
		PrimaryKey: primaryKeyColRe.MatchString(trimmed),
	}
	// A generated column's expression contains the word DEFAULT only by
	// coincidence, and its parenthesized body defeats the tail-anchored
	// default pattern — so it is never read for one.
	if !col.Generated {
		if m := columnDefaultRe.FindStringSubmatch(trimmed); m != nil {
			col.Default = strings.TrimSpace(m[1])
		}
	}
	return col, true
}

// columnTypeIn reads the declared type out of a column definition:
// everything between the column name and the first constraint keyword. A
// multi-word type survives whole ("TIMESTAMP WITH TIME ZONE", "DOUBLE
// PRECISION"), and a parenthesized precision stays attached
// ("NUMERIC(12, 2)"), because the classifier keys on the leading word and
// a truncated "NUMERIC" would still land correctly either way.
//
// Returns "" when the definition has no type it can isolate — which
// sqlValueShape reads as "unknown", the silent answer, rather than
// guessing.
func columnTypeIn(trimmed string, fields []string) string {
	if len(fields) < 2 {
		return ""
	}
	// Everything after the column name, before any constraint clause.
	//
	// The guard is not currently reachable: the only caller builds fields with
	// strings.Fields(trimmed), so fields[1] is always a substring. But that
	// invariant lives in parseColumnDef, one function away, and Index returning
	// -1 here would be a slice-bounds PANIC rather than a wrong answer — too
	// sharp an edge to leave resting on a caller's implementation detail.
	at := strings.Index(trimmed, fields[1])
	if at < 0 {
		return strings.TrimSpace(fields[1])
	}
	rest := strings.TrimSpace(trimmed[at:])
	m := sqlTypeHeadRE.FindStringSubmatch(rest)
	if m == nil {
		return strings.TrimSpace(fields[1])
	}
	return strings.TrimSpace(m[1])
}
