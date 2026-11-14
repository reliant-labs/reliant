package dbanalyze_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// base is the schema every test starts from: pre-existing tables with rows,
// as merged migrations would have left them.
var base = []dbanalyze.MigrationFile{{
	File: "db/migrations/1_base.up.sql", Version: 1,
	SQL: `CREATE TABLE p (id int PRIMARY KEY);
CREATE TABLE t (id int PRIMARY KEY, a int, v varchar(10), s text);
INSERT INTO p SELECT g FROM generate_series(1, 100) g;
INSERT INTO t SELECT g, g, 'x', 'y' FROM generate_series(1, 100) g;`,
}}

type run struct {
	migs     []dbanalyze.Migration
	findings []dbanalyze.Finding
	err      error
	dsn      string
}

// analyze runs files on a scratch database migrated to base and returns
// what the migration pass made of them.
func analyze(t *testing.T, cfg dbanalyze.Config, opts dbanalyze.SessionOptions, files ...string) run {
	t.Helper()
	ctx := context.Background()
	dsn, cleanup, err := dbanalyze.PrepareScratch(ctx, base)
	if err != nil {
		t.Fatalf("scratch: %v", err)
	}
	t.Cleanup(cleanup)
	s, err := dbanalyze.Open(ctx, dsn, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	info, err := s.Info(ctx, dbanalyze.TargetScratch, "scratch")
	if err != nil {
		t.Fatal(err)
	}
	var mfs []dbanalyze.MigrationFile
	for i, sql := range files {
		mfs = append(mfs, dbanalyze.MigrationFile{File: "m" + string(rune('a'+i)) + ".up.sql", Version: uint64(10 + i), SQL: sql})
	}
	migs, findings, err := dbanalyze.AnalyzeMigrations(ctx, s, info, mfs, cfg)
	return run{migs: migs, findings: findings, err: err, dsn: dsn}
}

func (r run) stmt(t *testing.T, file, index int) dbanalyze.StatementResult {
	t.Helper()
	if r.err != nil {
		t.Fatalf("analysis failed: %v", r.err)
	}
	return r.migs[file].Statements[index]
}

// rules lists the findings' rules at a line of a file, sorted.
func (r run) rules(file string, line int) []string {
	var out []string
	for _, f := range r.findings {
		if f.File == file && f.Line == line {
			out = append(out, f.Rule+":"+f.Relation)
		}
	}
	slices.Sort(out)
	return out
}

func rewritten(st dbanalyze.StatementResult) []string {
	var out []string
	for _, rw := range st.Rewrites {
		out = append(out, rw.Relation)
	}
	slices.Sort(out)
	return out
}

func acquired(st dbanalyze.StatementResult, relation string) []string {
	var out []string
	for _, l := range st.Locks {
		if l.Relation == relation && !l.HeldBefore {
			out = append(out, l.Mode)
		}
	}
	slices.Sort(out)
	return out
}

func seqScans(st dbanalyze.StatementResult, relation string) int64 {
	for _, s := range st.Scans {
		if s.Relation == relation {
			return s.SeqScan
		}
	}
	return 0
}

// What Postgres (the version pgtest boots, 18 by default) actually does, as
// the measurement sees it. Each case is one statement against the base.
func TestMeasuredBehaviour(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		sql       string
		rewrites  []string
		scanned   bool
		locksOnT  []string
		findingOn []string
	}{
		{name: "int to bigint rewrites the table and its index",
			sql: "ALTER TABLE t ALTER COLUMN a TYPE bigint", rewrites: []string{"t", "t_pkey"}, scanned: true,
			locksOnT:  []string{"AccessExclusiveLock", "ShareLock"},
			findingOn: []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleTableRewrite + ":t"}},
		// Postgres drops and recreates t_pkey under a new OID: the rebuild
		// is the rewrite's, not an index built without CONCURRENTLY.
		{name: "changing an indexed column's type rewrites the table and rebuilds its index",
			sql: "ALTER TABLE t ALTER COLUMN id TYPE bigint", rewrites: []string{"t", "t_pkey"}, scanned: true,
			locksOnT:  []string{"AccessExclusiveLock", "ShareLock"},
			findingOn: []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleTableRewrite + ":t"}},
		{name: "widening a varchar does not rewrite",
			sql: "ALTER TABLE t ALTER COLUMN v TYPE varchar(20)", locksOnT: []string{"AccessExclusiveLock"}},
		{name: "a constant default does not rewrite",
			sql: "ALTER TABLE t ADD COLUMN c int NOT NULL DEFAULT 5", locksOnT: []string{"AccessExclusiveLock"}},
		// transaction_timestamp() is the function SQL's NOW names. Its value
		// is never read: the case pins that a stable default is evaluated
		// once and stored as the column's missing value, so nothing here
		// depends on the clock.
		{name: "a stable default (transaction_timestamp, i.e. now) does not rewrite",
			sql: "ALTER TABLE t ADD COLUMN c timestamptz DEFAULT transaction_timestamp()", locksOnT: []string{"AccessExclusiveLock"}},
		{name: "a volatile default rewrites",
			sql: "ALTER TABLE t ADD COLUMN c double precision DEFAULT random()", rewrites: []string{"t", "t_pkey"}, scanned: true,
			locksOnT:  []string{"AccessExclusiveLock", "ShareLock"},
			findingOn: []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleTableRewrite + ":t"}},
		{name: "a NOT VALID check does not scan",
			sql: "ALTER TABLE t ADD CONSTRAINT ck CHECK (a > 0) NOT VALID", locksOnT: []string{"AccessExclusiveLock"}},
		{name: "a validated check scans under ACCESS EXCLUSIVE",
			sql: "ALTER TABLE t ADD CONSTRAINT ck CHECK (a > 0)", scanned: true, locksOnT: []string{"AccessExclusiveLock"},
			findingOn: []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleValidationScan + ":t"}},
		{name: "SET NOT NULL with no check scans",
			sql: "ALTER TABLE t ALTER COLUMN s SET NOT NULL", scanned: true, locksOnT: []string{"AccessExclusiveLock"},
			findingOn: []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleValidationScan + ":t"}},
		{name: "a foreign key validates both tables under SHARE ROW EXCLUSIVE",
			sql: "ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p (id)", scanned: true,
			locksOnT:  []string{"AccessShareLock", "ShareRowExclusiveLock"},
			findingOn: []string{dbanalyze.RuleValidationScan + ":p", dbanalyze.RuleValidationScan + ":t"}},
		{name: "a NOT VALID foreign key does not scan",
			sql: "ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p (id) NOT VALID", locksOnT: []string{"AccessShareLock", "ShareRowExclusiveLock"}},
		{name: "CREATE INDEX takes SHARE and scans",
			sql: "CREATE INDEX t_a ON t (a)", scanned: true, locksOnT: []string{"ShareLock"},
			findingOn: []string{dbanalyze.RuleBlockingIndexBuild + ":t"}},
		{name: "a backfill scans under a lock that does not block writes",
			sql: "UPDATE t SET s = 'z'", scanned: true, locksOnT: []string{"RowExclusiveLock"}},
		{name: "TRUNCATE is the textual lane's",
			sql: "TRUNCATE t", rewrites: []string{"t", "t_pkey"}, scanned: true, locksOnT: []string{"AccessExclusiveLock", "ShareLock"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, tc.sql)
			st := r.stmt(t, 0, 0)
			if !st.Executed {
				t.Fatalf("not executed: %+v", st)
			}
			if got := rewritten(st); !slices.Equal(got, tc.rewrites) {
				t.Errorf("rewrites = %v, want %v", got, tc.rewrites)
			}
			if got := seqScans(st, "t") > 0; got != tc.scanned {
				t.Errorf("t scanned = %v, want %v (scans %+v)", got, tc.scanned, st.Scans)
			}
			if got := acquired(st, "t"); !slices.Equal(got, tc.locksOnT) {
				t.Errorf("locks on t = %v, want %v", got, tc.locksOnT)
			}
			if got := r.rules("ma.up.sql", 1); !slices.Equal(got, tc.findingOn) {
				t.Errorf("findings = %v, want %v", got, tc.findingOn)
			}
		})
	}
}

// A migration file runs as one transaction: the ACCESS EXCLUSIVE lock its
// NOT VALID statement takes is held through the VALIDATE that follows, so
// splitting them only helps across files. Each file commits before the next,
// so the lock an earlier file took is released before a later one runs.
func TestLocksAreHeldThroughTheirFileOnly(t *testing.T) {
	t.Parallel()
	addNotValid := "ALTER TABLE t ADD CONSTRAINT ck CHECK (a > 0) NOT VALID;\n"
	validate := "ALTER TABLE t VALIDATE CONSTRAINT ck;\n"

	same := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, addNotValid+validate)
	st := same.stmt(t, 0, 1)
	if got := acquired(st, "t"); !slices.Equal(got, []string{"ShareUpdateExclusiveLock"}) {
		t.Errorf("VALIDATE acquired %v, want SHARE UPDATE EXCLUSIVE alone", got)
	}
	if got, want := same.rules("ma.up.sql", 2), []string{
		dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleValidationScan + ":t",
	}; !slices.Equal(got, want) {
		t.Errorf("same file: findings = %v, want %v", got, want)
	}

	split := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, addNotValid, validate)
	if len(split.findings) != 0 {
		t.Errorf("split across files: findings = %+v, want none", split.findings)
	}
	for _, l := range split.stmt(t, 1, 0).Locks {
		if l.HeldBefore {
			t.Errorf("the second file's VALIDATE lists a lock the first file took, which committed: %+v", l)
		}
	}
}

// The lock a statement needs is counted even when an earlier FILE took the
// same mode: that file committed and released it, so the CHECK takes ACCESS
// EXCLUSIVE afresh and validates under it.
func TestALockAnEarlierFileTookIsTakenAgain(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"ALTER TABLE t ADD COLUMN c int;",
		"ALTER TABLE t ADD CONSTRAINT ck CHECK (a > 0);")
	if got := acquired(r.stmt(t, 1, 0), "t"); !slices.Equal(got, []string{"AccessExclusiveLock"}) {
		t.Errorf("the CHECK acquired %v, want ACCESS EXCLUSIVE", got)
	}
	if got, want := r.rules("mb.up.sql", 1), []string{
		dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleValidationScan + ":t",
	}; !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}

// SET NOT NULL skips its scan when a valid CHECK already proves it.
func TestSetNotNullAfterAValidatedCheckDoesNotScan(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"ALTER TABLE t ADD CONSTRAINT s_nn CHECK (s IS NOT NULL) NOT VALID;",
		"ALTER TABLE t VALIDATE CONSTRAINT s_nn;",
		"ALTER TABLE t ALTER COLUMN s SET NOT NULL;")
	if n := seqScans(r.stmt(t, 2, 0), "t"); n != 0 {
		t.Errorf("SET NOT NULL scanned t %d time(s)", n)
	}
	if len(r.findings) != 0 {
		t.Errorf("findings = %+v, want none", r.findings)
	}
}

// A relation the run created is new: rewriting or indexing it is free.
func TestRelationsCreatedInTheRunAreExempt(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"CREATE TABLE n (id int, b int);",
		"ALTER TABLE n ALTER COLUMN id TYPE bigint;\nCREATE INDEX n_b ON n (b);")
	st := r.stmt(t, 1, 0)
	if len(st.Rewrites) != 1 || !st.Rewrites[0].New {
		t.Errorf("rewrites = %+v, want n marked new", st.Rewrites)
	}
	if len(r.findings) != 0 {
		t.Errorf("findings = %+v, want none", r.findings)
	}
}

// CREATE INDEX CONCURRENTLY runs on its own, as the migrator runs it: its
// lock is reported from its kind, and the index exists for what follows, so
// the recommended pattern (build the unique index concurrently, then attach
// it as a constraint) analyzes clean.
func TestConcurrentIndexRunsOnItsOwn(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"CREATE UNIQUE INDEX CONCURRENTLY t_a ON t (a);",
		"ALTER TABLE t ADD CONSTRAINT t_a_key UNIQUE USING INDEX t_a;")
	st := r.stmt(t, 0, 0)
	if !st.Executed || st.DurationMS <= 0 {
		t.Fatalf("CREATE UNIQUE INDEX CONCURRENTLY: %+v", st)
	}
	want := []dbanalyze.Lock{{Relation: "t", Mode: "ShareUpdateExclusiveLock", Static: true}}
	if !slices.Equal(st.Locks, want) {
		t.Errorf("locks = %+v, want %+v", st.Locks, want)
	}
	if !r.stmt(t, 1, 0).Executed {
		t.Errorf("ADD CONSTRAINT … USING INDEX: %+v", r.stmt(t, 1, 0))
	}
	if len(r.findings) != 0 {
		t.Errorf("findings = %+v, want none", r.findings)
	}
}

// A maintenance statement that cannot run in a transaction is judged like
// any other unless it really is non-blocking: VACUUM FULL, CLUSTER and a
// non-concurrent REINDEX rewrite under locks that block writes (VACUUM FULL
// and CLUSTER reads too), so they are findings; plain VACUUM and the
// CONCURRENTLY forms are not. The last file of each case is the one judged.
func TestMaintenanceStatementsAreJudged(t *testing.T) {
	t.Parallel()
	rewriteAndLock := []string{dbanalyze.RuleAccessExclusiveLock + ":t", dbanalyze.RuleTableRewrite + ":t"}
	cases := []struct {
		name     string
		files    []string
		findings []string
		locksOnT []string // the documented (static) locks reported on t
	}{
		{name: "VACUUM FULL", files: []string{"VACUUM FULL t;"}, findings: rewriteAndLock, locksOnT: []string{"AccessExclusiveLock"}},
		{name: "VACUUM (FULL)", files: []string{"VACUUM (FULL, ANALYZE) t;"}, findings: rewriteAndLock, locksOnT: []string{"AccessExclusiveLock"}},
		{name: "VACUUM (FULL false)", files: []string{"VACUUM (FULL false) t;"}},
		{name: "plain VACUUM", files: []string{"VACUUM t;"}},
		{name: "VACUUM ANALYZE", files: []string{"VACUUM ANALYZE t;"}},
		{name: "CLUSTER of every clustered table", files: []string{"ALTER TABLE t CLUSTER ON t_pkey;", "CLUSTER;"},
			findings: rewriteAndLock, locksOnT: []string{"AccessExclusiveLock"}},
		{name: "REINDEX SCHEMA", files: []string{"REINDEX SCHEMA public;"},
			findings: []string{dbanalyze.RuleTableRewrite + ":t"}, locksOnT: []string{"ShareLock"}},
		{name: "REINDEX TABLE in a transaction", files: []string{"REINDEX TABLE t;"},
			findings: []string{dbanalyze.RuleTableRewrite + ":t"}},
		{name: "REINDEX SCHEMA CONCURRENTLY", files: []string{"REINDEX SCHEMA CONCURRENTLY public;"}},
		{name: "REINDEX TABLE CONCURRENTLY", files: []string{"REINDEX TABLE CONCURRENTLY t;"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, tc.files...)
			last := len(tc.files) - 1
			st := r.stmt(t, last, 0)
			if !st.Executed {
				t.Fatalf("not executed: %+v", st)
			}
			file := "m" + string(rune('a'+last)) + ".up.sql"
			var onT []string
			for _, f := range r.rules(file, 1) {
				if strings.HasSuffix(f, ":t") {
					onT = append(onT, f)
				}
			}
			if !slices.Equal(onT, tc.findings) {
				t.Errorf("findings on t = %v, want %v (all: %+v)", onT, tc.findings, r.findings)
			}
			var static []string
			for _, l := range st.Locks {
				if l.Relation == "t" && l.Static {
					static = append(static, l.Mode)
				}
			}
			if tc.locksOnT != nil && !slices.Equal(static, tc.locksOnT) {
				t.Errorf("static locks on t = %v, want %v (%+v)", static, tc.locksOnT, st.Locks)
			}
			for _, f := range r.findings {
				if f.Relation == "t" && f.Rule == dbanalyze.RuleTableRewrite && strings.Contains(f.Fix, "column") {
					t.Errorf("a maintenance statement got the column-change fix: %q", f.Fix)
				}
			}
		})
	}
}

// DROP INDEX CONCURRENTLY has its effect too: a later migration may reuse the
// name.
func TestConcurrentIndexDropRunsOnItsOwn(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"CREATE INDEX CONCURRENTLY t_a ON t (a);",
		"DROP INDEX CONCURRENTLY t_a;",
		"CREATE INDEX CONCURRENTLY t_a ON t (a, s);")
	if r.err != nil {
		t.Fatal(r.err)
	}
	drop := r.stmt(t, 1, 0)
	want := []dbanalyze.Lock{{Relation: "t_a", Mode: "ShareUpdateExclusiveLock", Static: true}}
	if !drop.Executed || !slices.Equal(drop.Locks, want) {
		t.Errorf("DROP INDEX CONCURRENTLY: %+v", drop)
	}
	if len(r.findings) != 0 {
		t.Errorf("findings = %+v, want none", r.findings)
	}
}

// postgres runs a file of several statements as one transaction, so a
// nontransactional statement beside others fails as written; the analysis
// says so instead of running it differently than the migrator would.
func TestANontransactionalStatementBesideOthersIsRefused(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, "VACUUM t;\nALTER TABLE t ADD COLUMN c int;")
	if r.err == nil || !strings.Contains(r.err.Error(), "alone in its own migration file") {
		t.Fatalf("err = %v", r.err)
	}
	alone := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, "VACUUM t;")
	if st := alone.stmt(t, 0, 0); !st.Executed {
		t.Errorf("VACUUM alone: %+v", st)
	}
}

// A file that opens or ends its own transaction is not applied: its COMMIT
// would commit part of the file and end the measurement.
func TestAFileControllingItsTransactionIsRefused(t *testing.T) {
	t.Parallel()
	for _, sql := range []string{
		"BEGIN;\nCREATE TABLE n (id int);\nCOMMIT;",
		"CREATE TABLE n (id int);\nCOMMIT;\nALTER TABLE t ADD COLUMN c int;",
		"SAVEPOINT x;\nCREATE TABLE n (id int);",
	} {
		r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, sql)
		if r.err == nil || !strings.Contains(r.err.Error(), "controls its own transaction") {
			t.Errorf("%q: err = %v", sql, r.err)
			continue
		}
		if len(r.migs) != 1 || r.migs[0].Analyzed || len(r.migs[0].Statements) != 0 {
			t.Errorf("%q: migrations = %+v, want the file unapplied", sql, r.migs)
		}
	}
}

// A statement that changes what the whole server shares is not run on the
// copy: a role is not the database's to roll back or drop.
func TestServerWideStatementsAreNotRun(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"CREATE ROLE forge_analyze_never_created;\nALTER TABLE t ADD COLUMN c int;")
	st := r.stmt(t, 0, 0)
	if st.Executed || !strings.Contains(st.Reason, "whole server") {
		t.Errorf("CREATE ROLE: %+v", st)
	}
	if !r.stmt(t, 0, 1).Executed {
		t.Error("the statement after it was not executed")
	}
	db, err := sql.Open("pgx", r.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'forge_analyze_never_created')").Scan(&exists); err != nil || exists {
		t.Errorf("the role exists (%v, %v)", exists, err)
	}
}

// When a type change keeps an index's storage (a binary-compatible change),
// the recreated index is neither built nor rewritten.
func TestAnIndexWhoseStorageIsReusedIsNotRebuilt(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"CREATE INDEX t_v ON t (v);",
		"ALTER TABLE t ALTER COLUMN v TYPE varchar(20);")
	if got := rewritten(r.stmt(t, 1, 0)); len(got) != 0 {
		t.Errorf("rewrites = %v, want none", got)
	}
	if got := r.rules("mb.up.sql", 1); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
}

func TestSizesDecideWhetherAFindingIsKept(t *testing.T) {
	t.Parallel()
	rewrite := "ALTER TABLE t ALTER COLUMN a TYPE bigint"

	small := analyze(t, dbanalyze.Config{TableRows: map[string]int64{"t": 500}}, dbanalyze.SessionOptions{}, rewrite)
	if len(small.findings) != 0 {
		t.Errorf("a table hinted below small_table_rows kept findings: %+v", small.findings)
	}
	if rc := small.stmt(t, 0, 0).Rows["t"]; rc.Source != dbanalyze.RowsFromHints || rc.Value == nil || *rc.Value != 500 {
		t.Errorf("rows[t] = %+v, want 500 from table_rows", rc)
	}

	large := analyze(t, dbanalyze.Config{TableRows: map[string]int64{"t": 40_000_000}}, dbanalyze.SessionOptions{}, rewrite)
	if len(large.findings) != 2 {
		t.Fatalf("findings = %+v, want the rewrite and the lock", large.findings)
	}
	for _, f := range large.findings {
		if f.Rows == nil || *f.Rows.Value != 40_000_000 || f.Severity != dbanalyze.SeverityError {
			t.Errorf("finding %s: rows %+v severity %s", f.Rule, f.Rows, f.Severity)
		}
	}

	// A scratch target's statistics never size a table, even where
	// pg_class has an estimate: its tables are empty in production terms.
	unknown := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{}, rewrite)
	if rc := unknown.stmt(t, 0, 0).Rows["t"]; rc.Source != dbanalyze.RowsUnknown || rc.Value != nil {
		t.Errorf("rows[t] = %+v, want unknown", rc)
	}
}

func TestATimeoutIsAFindingAndEndsTheRun(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{StatementTimeout: 100 * time.Millisecond},
		"ALTER TABLE t ADD COLUMN c int;\nSELECT pg_sleep(5);\nALTER TABLE t ADD COLUMN d int;",
		"ALTER TABLE t ADD COLUMN e int;")
	if r.err != nil {
		t.Fatalf("a timeout is a finding, not a failed analysis: %v", r.err)
	}
	if got := r.rules("ma.up.sql", 2); !slices.Equal(got, []string{dbanalyze.RuleStatementTimeout + ":"}) {
		t.Errorf("findings = %v", got)
	}
	if st := r.migs[0].Statements[2]; st.Executed || st.Reason == "" {
		t.Errorf("the statement after the timeout: %+v", st)
	}
	if m := r.migs[1]; m.Analyzed || !strings.Contains(m.Reason, "statement_timeout") {
		t.Errorf("the next migration: %+v", m)
	}
}

func TestAFailedStatementCannotBeAnalyzedPast(t *testing.T) {
	t.Parallel()
	r := analyze(t, dbanalyze.Config{}, dbanalyze.SessionOptions{},
		"ALTER TABLE missing ADD COLUMN c int;", "ALTER TABLE t ADD COLUMN e int;")
	var se *dbanalyze.StatementError
	if !errors.As(r.err, &se) || se.SQLState != "42P01" || se.Line != 1 {
		t.Fatalf("err = %v, want a StatementError for 42P01 at line 1", r.err)
	}
	if len(r.migs) != 1 || r.migs[0].Statements[0].SQLState != "42P01" {
		t.Errorf("migrations = %+v", r.migs)
	}
}

// A lock another session holds is waited on for lock_timeout, then the
// analysis stops: the target is in use.
func TestALockHeldElsewhereStopsTheAnalysis(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn, cleanup, err := dbanalyze.PrepareScratch(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	other, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	otx, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otx.Rollback() }()
	if _, err := otx.ExecContext(ctx, "LOCK TABLE t IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}

	s, err := dbanalyze.Open(ctx, dsn, dbanalyze.SessionOptions{LockTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, _, err = dbanalyze.AnalyzeMigrations(ctx, s, dbanalyze.TargetInfo{Kind: dbanalyze.TargetDSN}, []dbanalyze.MigrationFile{
		{File: "a.up.sql", Version: 2, SQL: "ALTER TABLE t ADD COLUMN c int"},
	}, dbanalyze.Config{})
	var se *dbanalyze.StatementError
	if !errors.As(err, &se) || se.SQLState != "55P03" || !strings.Contains(err.Error(), "the target is in use") {
		t.Fatalf("err = %v, want a lock_timeout StatementError", err)
	}
}

func TestMigrationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	empty, cleanup, err := dbanalyze.PrepareScratch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	migrated, cleanup2, err := dbanalyze.PrepareScratch(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup2)
	for dsn, want := range map[string]dbanalyze.MigrationState{
		empty:    {},
		migrated: {Applied: true, Version: 1},
	} {
		s, err := dbanalyze.Open(ctx, dsn, dbanalyze.SessionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.MigrationState(ctx)
		_ = s.Close()
		if err != nil || got != want {
			t.Errorf("state = %+v, %v; want %+v", got, err, want)
		}
	}
}

func TestDocumentFinalize(t *testing.T) {
	d := dbanalyze.NewDocument()
	d.Finalize()
	if d.ExitCode != dbanalyze.ExitOK || d.Summary.MigrationsScanned != 0 {
		t.Errorf("empty: %+v", d)
	}
	d.Migrations = []dbanalyze.Migration{{Analyzed: true, Statements: []dbanalyze.StatementResult{{Executed: true}, {}}}}
	d.Findings = []dbanalyze.Finding{{Severity: dbanalyze.SeverityWarning}}
	d.Finalize()
	if d.ExitCode != dbanalyze.ExitOK || d.Summary.MigrationsScanned != 1 || d.Summary.StatementsExecuted != 1 {
		t.Errorf("warning only: %+v", d)
	}
	d.Findings = append(d.Findings, dbanalyze.Finding{Severity: dbanalyze.SeverityError})
	d.Finalize()
	if d.ExitCode != dbanalyze.ExitFindings {
		t.Errorf("an error finding: exit %d", d.ExitCode)
	}
	d.Error = "no postgres"
	d.Finalize()
	if d.ExitCode != dbanalyze.ExitCouldNotAnalyze {
		t.Errorf("a failed analysis: exit %d", d.ExitCode)
	}
}

func TestNonConcurrently(t *testing.T) {
	st := dbanalyze.SplitStatements("-- x\ncreate unique index  concurrently if not exists i ON t (a);")[0]
	got, ok := st.NonConcurrently()
	if !ok || got != "create unique index  if not exists i ON t (a)" {
		t.Errorf("NonConcurrently = %q, %v", got, ok)
	}
	if _, ok := dbanalyze.SplitStatements("CREATE INDEX i ON t (a)")[0].NonConcurrently(); ok {
		t.Error("a plain CREATE INDEX was rewritten")
	}
}
