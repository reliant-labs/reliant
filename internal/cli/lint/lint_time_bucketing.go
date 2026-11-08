// File: internal/cli/lint/lint_time_bucketing.go
//
// time-bucketing — `forge lint --time-bucketing`.
//
// The reporting-query twin of read-only-fields: a defect that produces
// wrong numbers with no error, no failing test and no log line anywhere.
//
// forge maps `google.protobuf.Timestamp` to `TIMESTAMPTZ`, which is
// correct — an instant should be stored as an instant. But the moment a
// reporting query buckets one:
//
//	date_trunc('day', recorded_at)
//
// postgres truncates in the SESSION's timezone, and the driver sets that
// from the CLIENT HOST. So "distance per day" silently means a different
// day depending on where the server process happens to run. Measured
// against real postgres over five samples spanning two UTC calendar days
// (pkg/pgtest/datetrunc_premise_pg_test.go):
//
//	session TZ=UTC               2 buckets
//	session TZ=America/New_York  2 buckets, boundaries at 05:00Z
//	session TZ=Asia/Tokyo        3 buckets
//
// Same rows, same query, three different answers. The three-argument form
// pins the zone in the query and returns 2 under every session zone:
//
//	date_trunc('day', recorded_at, 'UTC')
//
// ── Why this is worth a rule ──────────────────────────────────────────────
//
// Nothing fails. No constraint is violated, no type error is raised, no
// test breaks — a weekly chart still renders, its bars merely attributed
// to the wrong days by a fraction of a day that CHANGES when the app is
// deployed to a differently-configured host. A CI box on UTC and a laptop
// on UTC-5 produce different numbers from identical rows, which reads as
// flakiness rather than as a timezone bug, so the investigation starts in
// the wrong place.
//
// It is also squarely in forge's path rather than incidental to it. The
// generated ORM cannot express GROUP BY / SUM, so forge itself routes
// authors to raw SQL for any reporting screen — which is exactly where
// this construct lives.
//
// ── What it reads ────────────────────────────────────────────────────────
//
// Purely textual, no database, matching read-only-fields and
// fixture-drift. Two SQL sources:
//
//   - Go string literals in non-generated, non-test source. A handler's
//     query is a string constant, so the literal IS the query.
//   - The project's .up.sql migrations, where a reporting VIEW lives once
//     the query stops being ad-hoc.
//
// Column types come from the same migration replay the read-only rule
// depends on (readOnlyColumnsFromMigrations), so the two agree exactly on
// what the schema says.
//
// ── The exclusions, and why each one exists ──────────────────────────────
//
// A rule that invents findings gets globally disabled, and then every real
// signal it would have reported is lost too. A rule that misses a case
// costs one defect. So every ambiguity resolves to SILENCE:
//
//  1. THREE arguments → silent. That is the fix. Firing on it would tell
//     the author to undo the correct thing. A named zone other than UTC is
//     equally silent: the author has stated the zone, which is the entire
//     thing this rule asks for.
//  2. The truncated argument is not a bare column reference → silent. A
//     cast, a function call, a placeholder, or `AT TIME ZONE 'UTC'` all
//     have types this text parse cannot read — and the last of those is
//     itself a correct fix, since it converts to TIMESTAMP first.
//  3. The column resolves to no declared type → silent. A gap in this
//     parser is not evidence about the app.
//  4. The column is TIMESTAMP WITHOUT TIME ZONE → silent. Postgres
//     truncates the stored wall-clock value; every host agrees. Verified
//     alongside the firing case.
//  5. The column name is declared with CONFLICTING types across tables →
//     silent. A bare `recorded_at` cannot be attributed to a table from
//     text, and guessing would report a correct query half the time.
//     Columns are resolved by NAME rather than by table precisely because
//     the enclosing FROM clause is not reliably parseable from a fragment;
//     requiring agreement across every declaration is what makes that
//     sound.
//  6. The unit is not a string literal → silent. A runtime unit might be
//     'minute'.
//  7. The unit is finer than an hour → silent. Every offset in the tz
//     database is a whole number of minutes, so a minute or second bucket
//     cannot straddle a zone boundary.
//
// ── Severity: warning, never gating ──────────────────────────────────────
//
// Matching fixture-drift, crud-fixtures and guarded-fields. Unlike the
// read-only rule — which gates because an unwritten column is
// unambiguously a defect — a local-time bucket can be exactly what the
// author wants for a shift report or a business-day rollup. The rule's job
// is to make the choice visible, not to make it for them.

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
)

// timeBucketRuleID is the stable rule id, reported in text mode and in
// `forge lint --json`.
const timeBucketRuleID = "forge-time-bucket-session-tz"

// timeBucketFinding is one two-argument date_trunc over a TIMESTAMPTZ
// column. File is root-relative; Line and Col are 1-indexed and point at
// the `date_trunc` call itself, which is the text the author edits.
type timeBucketFinding struct {
	File string
	Line int
	Col  int

	// Unit is the truncation unit as written ("day", "week"), and Column
	// the bare column reference being truncated. Both appear verbatim in
	// the fix hint, so the suggested replacement is the author's own line
	// rather than a generic template.
	Unit   string
	Column string

	// Table is where the column was declared TIMESTAMPTZ, carried so the
	// message can name the schema fact the finding rests on. Findings
	// resolve columns by name (see exclusion 5), so this is the single
	// table that declares it.
	Table string
}

// message is the one-line summary, shared by text mode and JSON.
func (f timeBucketFinding) message() string {
	return fmt.Sprintf(
		"date_trunc('%s', %s) truncates in the session's timezone, and %s.%s is TIMESTAMPTZ",
		f.Unit, f.Column, f.Table, f.Column)
}

// timeBucketFixHint renders the remediation.
//
// It names the deploy-host dependence explicitly, because that is the fact
// that makes this worth reading: without it the finding looks like a style
// note about an SQL function, and the author has no reason to act. The
// suggested replacement is spelled out in full with the author's own unit
// and column so it can be pasted over the offending call.
func timeBucketFixHint(f timeBucketFinding) string {
	return fmt.Sprintf(
		"%s.%s is TIMESTAMPTZ, and two-argument `date_trunc` truncates it in the SESSION's "+
			"timezone — which the driver sets from the client host. So this bucket boundary "+
			"moves with wherever the process runs: the same rows produce different totals on a "+
			"UTC CI box and a UTC-5 laptop, with every number attributed to the wrong %s by a "+
			"fraction of one. Nothing fails — no constraint, no type error, no failing test — so "+
			"the only symptom is a chart that disagrees with itself between deploys, which reads "+
			"as flakiness rather than as a timezone bug. Pin the zone in the query: "+
			"`date_trunc('%s', %s, 'UTC')`. If the bucket is genuinely meant to follow local "+
			"time — a shift report, a business-day rollup — name that zone instead "+
			"(`date_trunc('%s', %s, 'America/New_York')`); either spelling is explicit and "+
			"silences this check. TIMESTAMPTZ remains the right column type: the instant is "+
			"stored correctly, and only the bucketing needs a zone.",
		f.Table, f.Column, f.Unit, f.Unit, f.Column, f.Unit, f.Column)
}

// collectTimeBucketFindings is the shared engine behind text mode and
// `forge lint --json`.
//
// A project with no migrations yields nothing: without a schema there is
// no column type to resolve against, and a date_trunc whose argument type
// is unknown is exclusion 3.
func collectTimeBucketFindings(root, migrationsDir string) ([]timeBucketFinding, error) {
	if !filepath.IsAbs(migrationsDir) {
		migrationsDir = filepath.Join(root, migrationsDir)
	}
	columns, err := readOnlyColumnsFromMigrations(migrationsDir)
	if err != nil {
		return nil, err
	}
	types := timestampColumnTypes(columns)
	if len(types) == 0 {
		return nil, nil
	}

	var findings []timeBucketFinding
	fromGo, err := timeBucketsInGoLiterals(root, types)
	if err != nil {
		return nil, err
	}
	findings = append(findings, fromGo...)

	err = eachMigration(root, migrationsDir, func(relPath, content string) {
		findings = append(findings, timeBucketsIn(relPath, content, 0, 0, types)...)
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Col < findings[j].Col
	})
	return findings, nil
}

// columnTimestampKind is what this rule needs to know about one column
// name across the WHOLE schema.
type columnTimestampKind struct {
	// table is the single table declaring this column, "" once more than
	// one declares it with disagreeing types.
	table string
	// sessionDependent is true for TIMESTAMPTZ, false for TIMESTAMP
	// WITHOUT TIME ZONE.
	sessionDependent bool
	// ambiguous records that two tables declare this name with different
	// timestamp kinds, which makes a bare reference unattributable.
	ambiguous bool
}

// timestampColumnTypes indexes every timestamp column in the schema BY
// NAME, across all tables.
//
// By name rather than by table because a SQL fragment in a Go constant does
// not reliably expose its FROM clause to a text parse — a CTE, a join, or a
// query assembled from pieces all defeat it. Resolving by name is sound
// only because disagreement is treated as unresolvable: a name that two
// tables declare with different timestamp kinds is marked ambiguous and
// yields silence (exclusion 5). A name every table declares the same way
// has the same answer regardless of which one the query meant.
//
// Non-timestamp columns are omitted entirely, so a date_trunc over a
// BIGINT — which postgres rejects at parse time anyway — is simply
// unresolvable here.
func timestampColumnTypes(columns map[string]map[string]sqlColumn) map[string]columnTimestampKind {
	out := map[string]columnTimestampKind{}
	for table, cols := range columns {
		for name, col := range cols {
			dependent, ok := timestamptzType(col.Type)
			if !ok {
				continue
			}
			prior, seen := out[name]
			if !seen {
				out[name] = columnTimestampKind{table: table, sessionDependent: dependent}
				continue
			}
			if prior.ambiguous || prior.sessionDependent != dependent {
				out[name] = columnTimestampKind{ambiguous: true}
			}
			// Two tables agreeing on the kind is not ambiguity: either
			// answer produces the same verdict. The recorded table is
			// whichever was seen first, which is only ever used to name a
			// declaration site in the message.
		}
	}
	return out
}

// timestamptzType classifies a declared column type. ok=false for anything
// that is not a timestamp at all.
//
// Both postgres spellings are recognized on each side: TIMESTAMPTZ and
// TIMESTAMP WITH TIME ZONE are the same type, as are TIMESTAMP and
// TIMESTAMP WITHOUT TIME ZONE. A precision qualifier (`TIMESTAMPTZ(3)`)
// sits between the head and the zone clause, so the test is on the
// normalized string rather than on a prefix.
func timestamptzType(declared string) (sessionDependent bool, ok bool) {
	t := strings.ToUpper(strings.Join(strings.Fields(declared), " "))
	if !strings.HasPrefix(t, "TIMESTAMP") {
		return false, false
	}
	switch {
	case strings.Contains(t, "WITHOUT TIME ZONE"):
		return false, true
	case strings.HasPrefix(t, "TIMESTAMPTZ"), strings.Contains(t, "WITH TIME ZONE"):
		return true, true
	default:
		// Bare TIMESTAMP is WITHOUT TIME ZONE, per the SQL standard and
		// postgres's default.
		return false, true
	}
}

// dateTruncCallRe matches a `date_trunc(` call head. The arguments are read
// by balanced-paren scan rather than by pattern, so a nested call or a cast
// inside them cannot truncate the match.
var dateTruncCallRe = regexp.MustCompile(`(?i)\bdate_trunc\s*\(`)

// bareColumnRefRe matches an argument that is nothing but an identifier,
// optionally table-qualified. Anything else — a cast, a call, an AT TIME
// ZONE, a placeholder — fails to match and is exclusion 2.
var bareColumnRefRe = regexp.MustCompile(`^"?[A-Za-z_]\w*"?(?:\."?[A-Za-z_]\w*"?)?$`)

// sqlStringLiteralRe matches a single-quoted SQL literal occupying the
// whole argument, which is the only shape the unit may take.
var sqlStringLiteralRe = regexp.MustCompile(`^'([^']*)'$`)

// subHourUnits are the truncation units that cannot straddle a timezone
// boundary, because every offset in the tz database is a whole number of
// minutes. Bucketing by one of these is session-independent in practice,
// so it is exclusion 7.
var subHourUnits = map[string]bool{
	"minute": true, "minutes": true, "min": true, "mins": true, "m": true,
	"second": true, "seconds": true, "sec": true, "secs": true, "s": true,
	"millisecond": true, "milliseconds": true, "ms": true,
	"microsecond": true, "microseconds": true, "us": true,
}

// timeBucketsIn finds every offending date_trunc in one SQL text.
//
// lineBase and colBase locate the text inside its containing file: for a
// migration they are zero (the content IS the file), and for a Go string
// literal they carry the literal's own position so a finding addresses the
// source line the author edits rather than an offset into a fragment.
func timeBucketsIn(
	relPath, sql string,
	lineBase, colBase int,
	types map[string]columnTimestampKind,
) []timeBucketFinding {
	var findings []timeBucketFinding
	for _, m := range dateTruncCallRe.FindAllStringIndex(sql, -1) {
		open := m[1] - 1 // the '(' the pattern ends on
		close, ok := matchParen(sql, open)
		if !ok {
			continue
		}
		args := splitTopLevel(sql[open+1 : close])
		// THREE arguments is the fix (exclusion 1). Anything other than
		// exactly two is not the shape this rule speaks about.
		if len(args) != 2 {
			continue
		}

		unitMatch := sqlStringLiteralRe.FindStringSubmatch(strings.TrimSpace(args[0]))
		if unitMatch == nil {
			continue // exclusion 6: a runtime unit might be 'minute'
		}
		unit := strings.TrimSpace(unitMatch[1])
		if subHourUnits[strings.ToLower(unit)] {
			continue // exclusion 7
		}

		ref := strings.TrimSpace(args[1])
		if !bareColumnRefRe.MatchString(ref) {
			continue // exclusion 2
		}
		column := normIdent(ref)
		kind, known := types[column]
		if !known || kind.ambiguous || !kind.sessionDependent {
			// exclusions 3, 5 and 4 respectively.
			continue
		}

		line, col := positionIn(sql, m[0], lineBase, colBase)
		findings = append(findings, timeBucketFinding{
			File: relPath, Line: line, Col: col,
			Unit: unit, Column: column, Table: kind.table,
		})
	}
	return findings
}

// positionIn converts a byte offset inside a SQL fragment to a 1-indexed
// line and column in the containing file.
//
// lineBase is the file line the fragment starts on and colBase the column
// it starts at, both 1-indexed, or both zero when the fragment IS the file.
// Only an offset on the fragment's FIRST line inherits colBase; a later
// line starts at column 1 of the file, since the literal's own newlines are
// the file's.
func positionIn(fragment string, off, lineBase, colBase int) (line, col int) {
	if lineBase == 0 {
		lineBase, colBase = 1, 1
	}
	before := fragment[:off]
	newlines := strings.Count(before, "\n")
	line = lineBase + newlines
	if i := strings.LastIndexByte(before, '\n'); i >= 0 {
		return line, off - i
	}
	return line, colBase + off
}

// timeBucketsInGoLiterals scans the project's non-generated, non-test Go
// source for SQL string literals carrying an offending date_trunc.
//
// Only string literals are examined, never the file at large, which is what
// makes a date_trunc in a Go comment or in prose structurally incapable of
// producing a finding rather than merely filtered.
//
// Generated files are excluded for the usual reason — they are forge's
// output, and a finding there is a report about forge rather than about the
// project — and _test.go files because a test may deliberately construct
// the broken form to pin behaviour, which is exactly what this package's
// own tests do.
func timeBucketsInGoLiterals(root string, types map[string]columnTimestampKind) ([]timeBucketFinding, error) {
	var findings []timeBucketFinding
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
		rel := relToProject(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			pos := fset.Position(lit.Pos())
			// +1 for the opening quote or backtick, so an offset into the
			// unquoted text lines up with the source column.
			findings = append(findings, timeBucketsIn(rel, text, pos.Line, pos.Column+1, types)...)
			return true
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return findings, nil
}
