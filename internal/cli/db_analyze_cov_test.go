package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

func covBAnalyzeDoc() *dbanalyze.Document {
	d := dbanalyze.NewDocument()
	d.Target = dbanalyze.TargetInfo{Kind: dbanalyze.TargetDSN, Identity: "db.internal:5432/snap", ServerVersion: "16.2",
		Clone: &dbanalyze.CloneInfo{Database: "forge_clone_1", SizeBytes: 12_500_000, DurationMS: 340}}
	d.Base = "main@abc1234"
	d.Migrations = []dbanalyze.Migration{
		{File: "db/migrations/001_add.up.sql", Analyzed: true, Statements: []dbanalyze.StatementResult{
			{Line: 1, SQL: "ALTER TABLE orders\n  ADD COLUMN note text", Executed: true, DurationMS: 12.34,
				Locks: []dbanalyze.Lock{
					{Relation: "orders", Mode: "AccessExclusiveLock"},
					{Relation: "orders", Mode: "ShareLock", Static: true},
					{Relation: "orders_pkey", Mode: "RowExclusiveLock", HeldBefore: true},
					{Relation: "fresh", Mode: "AccessExclusiveLock", New: true},
				},
				Rewrites: []dbanalyze.Rewrite{{Relation: "orders", Kind: "table"}, {Relation: "fresh", Kind: "table", New: true}},
				Scans:    []dbanalyze.Scan{{Relation: "orders", SeqScan: 1, SeqTupRead: 40_000_000}, {Relation: "fresh", New: true}}},
			{Line: 4, SQL: "CREATE SERVER x", Executed: false, Reason: "changes what the whole server shares"},
			{Line: 6, SQL: "DROP TABLE nope", Error: "relation \"nope\" does not exist"},
		}},
		{File: "db/migrations/002_later.up.sql", Analyzed: false, Reason: "an earlier statement failed"},
	}
	d.Queries = []dbanalyze.Query{
		{SQL: "SELECT * FROM orders WHERE note = $1", QueryDetail: &dbanalyze.QueryDetail{
			Callers: []querycapture.CallSite{{Caller: querycapture.Frame{File: "internal/orders/repo.go", Line: 42}}},
			Access: []dbanalyze.TableAccess{
				{Table: "orders", Verdict: dbanalyze.AccessUnserved, How: dbanalyze.UnservedFilter, Detail: "note = $1", SeqScan: true},
				{Table: "users", Verdict: dbanalyze.AccessServed, How: dbanalyze.ServedByIndexCond, Index: "users_pkey"},
			}}},
		{SQL: "SELECT bogus", Error: "column \"bogus\" does not exist"},
		{SQL: "SELECT 1", QueryDetail: &dbanalyze.QueryDetail{Access: []dbanalyze.TableAccess{{Table: "t", Verdict: dbanalyze.AccessUnfiltered}}}},
	}
	d.Findings = []dbanalyze.Finding{
		{Rule: "table-rewrite", Severity: dbanalyze.SeverityError, File: "db/migrations/001_add.up.sql", Line: 1, Message: "rewrites orders", Fix: "add the column nullable first"},
		{Rule: "seq-scan", Severity: "warning", File: "internal/orders/repo.go", Line: 42, Message: "no index", Note: "new in v1.11.0; gates from v1.12.0"},
	}
	d.Summary.QueriesCaptured, d.Summary.QueriesScanned, d.Summary.QueriesReason = 3, 2, "one shape failed to plan"
	d.Summary.Skipped = []dbanalyze.Skipped{{What: "query abc", Reason: "no table"}}
	d.Finalize()
	return d
}

func TestWriteDBAnalyzeTextRendersEveryPart(t *testing.T) {
	var b bytes.Buffer
	writeDBAnalyzeText(&b, covBAnalyzeDoc())
	out := b.String()
	for _, want := range []string{
		"forge db analyze — dsn target db.internal:5432/snap (postgres 16.2), base main@abc1234",
		"analyzed on a copy, forge_clone_1 (12.5 MB, copied in 340ms), dropped afterwards",
		"db/migrations/001_add.up.sql",
		"line 1: ALTER TABLE orders ADD COLUMN note text",
		"12.3ms",
		"locks:    orders AccessExclusiveLock, orders ShareLock (known from the statement's kind)",
		"held:     orders_pkey RowExclusiveLock (taken by an earlier statement of the file)",
		"rewrote:  orders (table)",
		"scanned:  orders (1 seq scan(s), 40000000 rows)",
		"not executed: changes what the whole server shares",
		`failed: relation "nope" does not exist`,
		"not analyzed: an earlier statement failed",
		"internal/orders/repo.go:42  SELECT * FROM orders WHERE note = $1",
		"orders               unserved (filter): note = $1; the plan seq-scans it",
		"users                served (index_cond users_pkey)",
		`not planned: column "bogus" does not exist`,
		"(no caller)  SELECT 1",
		"t                    unfiltered",
		"findings:",
		"error db/migrations/001_add.up.sql:1 [table-rewrite] rewrites orders",
		"fix: add the column nullable first",
		"new in v1.11.0; gates from v1.12.0",
		"2 of 3 captured query shape(s) planned — one shape failed to plan",
		"skipped query abc: no table",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	// New relations are free: neither their locks, rewrites nor scans are listed.
	for _, unwanted := range []string{"fresh"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a relation created in the run must not be reported (%q):\n%s", unwanted, out)
		}
	}
}

// A Query built without its detail (the type documents the nil case) must
// render, not panic.
func TestWriteDBAnalyzeQueriesTextToleratesAQueryWithoutDetail(t *testing.T) {
	d := dbanalyze.NewDocument()
	d.Queries = []dbanalyze.Query{{SQL: "SELECT 1"}}
	var b bytes.Buffer
	writeDBAnalyzeQueriesText(&b, d)
	if !strings.Contains(b.String(), "(no caller)  SELECT 1") {
		t.Errorf("output = %q", b.String())
	}
}

func TestWriteDBAnalyzeTextSummaryLines(t *testing.T) {
	d := dbanalyze.NewDocument()
	d.Target.Kind = dbanalyze.TargetScratch
	d.Summary.Reason = "nothing pending"
	var b bytes.Buffer
	writeDBAnalyzeText(&b, d)
	out := b.String()
	if !strings.Contains(out, "forge db analyze — scratch target\n") || !strings.Contains(out, "0 migration(s) analyzed, 0 statement(s) executed — nothing pending") {
		t.Errorf("empty analysis:\n%s", out)
	}
	if strings.Contains(out, "captured query shape") {
		t.Errorf("no query pass was asked for:\n%s", out)
	}
}

func TestReportDBAnalyzeExitCodes(t *testing.T) {
	// OK: nothing gating.
	ok := dbanalyze.NewDocument()
	var b bytes.Buffer
	if err := reportDBAnalyze(&b, ok, false); err != nil {
		t.Errorf("a clean document is exit 0, got %v", err)
	}

	// Findings gate: the count is the error-severity findings only.
	var out bytes.Buffer
	err := reportDBAnalyze(&out, covBAnalyzeDoc(), false)
	var exit exitCodeError
	if !errors.As(err, &exit) || exit.code != dbanalyze.ExitFindings || exit.msg != "db analyze: 1 finding(s) gate" {
		t.Errorf("findings = %v; want exit %d naming 1 gating finding", err, dbanalyze.ExitFindings)
	}

	// --json prints the document and keeps the exit code.
	out.Reset()
	err = reportDBAnalyze(&out, covBAnalyzeDoc(), true)
	var round dbanalyze.Document
	if jerr := json.Unmarshal(out.Bytes(), &round); jerr != nil || round.Schema != dbanalyze.SchemaVersion || len(round.Findings) != 2 {
		t.Errorf("--json output = %v\n%s", jerr, out.String())
	}
	if !errors.As(err, &exit) || exit.code != dbanalyze.ExitFindings {
		t.Errorf("--json keeps the exit status: %v", err)
	}

	// Could not analyze: the error carries the document's reason.
	bad := dbanalyze.NewDocument()
	bad.ExitCode, bad.Error = dbanalyze.ExitCouldNotAnalyze, "cannot reach postgres"
	err = reportDBAnalyze(&out, bad, false)
	if !errors.As(err, &exit) || exit.code != dbanalyze.ExitCouldNotAnalyze || !strings.Contains(exit.msg, "db analyze could not analyze: cannot reach postgres") {
		t.Errorf("could-not-analyze = %v", err)
	}
}
