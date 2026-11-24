package dbanalyze_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

// querySchema is what the query tests plan against. orders.org_id is
// indexed, orders.customer_id and customers.org_id are not, and orders_open
// is a partial index only a literal `status = 'open'` can use.
var querySchema = []dbanalyze.MigrationFile{{
	File: "db/migrations/1_schema.up.sql", Version: 1,
	SQL: `CREATE TABLE orgs (id bigint PRIMARY KEY, name text);
CREATE TABLE customers (id bigint PRIMARY KEY, org_id bigint, name text);
CREATE TABLE orders (id bigint PRIMARY KEY, org_id bigint REFERENCES orgs (id), customer_id bigint,
  status text, created_at timestamptz);
CREATE INDEX orders_org ON orders (org_id);
CREATE INDEX orders_open ON orders (created_at) WHERE status = 'open';`,
}}

// querySession is a session on a scratch database at querySchema.
func querySession(t *testing.T) (*dbanalyze.Session, dbanalyze.TargetInfo) {
	t.Helper()
	ctx := context.Background()
	dsn, cleanup, err := dbanalyze.PrepareScratch(ctx, querySchema)
	if err != nil {
		t.Fatalf("scratch: %v", err)
	}
	t.Cleanup(cleanup)
	s, err := dbanalyze.Open(ctx, dsn, dbanalyze.SessionOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	info, err := s.Info(ctx, dbanalyze.TargetScratch, "scratch")
	if err != nil {
		t.Fatal(err)
	}
	return s, info
}

func shape(sql string) querycapture.Shape {
	st := querycapture.Normalize(sql)
	return querycapture.Shape{Fingerprint: st.Fingerprint, SQL: st.SQL, Operation: st.Operation, Tables: st.Tables,
		Callers: []querycapture.CallSite{{Caller: querycapture.Frame{File: "internal/db/orders_repo_ext.go", Line: 42, Function: "x"},
			Generated: &querycapture.Frame{File: "internal/db/orders_gen.go", Line: 7}, Tests: []string{"TestOrders"}, Count: 1}},
		Count: 1}
}

// access is "verdict/how index" per table of q, e.g.
// "orders=served/index_cond orders_org".
func access(q dbanalyze.Query) map[string]string {
	out := map[string]string{}
	for _, a := range q.Access {
		v := a.Verdict
		if a.How != "" {
			v += "/" + a.How
		}
		if a.Index != "" {
			v += " " + a.Index
		}
		out[a.Table] = v
	}
	return out
}

// The served/unserved classification, pinned on real postgres plans of an
// empty, never-analyzed schema (the scratch target): each case is a shape a
// reviewer would want called out, or one that must not be.
func TestAnalyzeQueriesClassifiesIndexUse(t *testing.T) {
	s, info := querySession(t)
	cases := []struct {
		name string
		sql  string
		want map[string]string
	}{
		{"filter served by an index", `SELECT * FROM orders WHERE org_id = $1`,
			map[string]string{"orders": "served/index_cond orders_org"}},
		{"filter with no index", `SELECT * FROM orders WHERE customer_id = $1`,
			map[string]string{"orders": "unserved/filter"}},
		{"primary key", `SELECT * FROM orders WHERE id = $1`,
			map[string]string{"orders": "served/index_cond orders_pkey"}},
		// The trap: with sequential scans off, the planner walks customers'
		// primary key in full and filters every row. An index node alone
		// proves nothing; only an Index Cond does. And orders, read in full
		// to be joined on customer_id, has no index on it.
		{"join on an unindexed foreign key, filter through an unrelated index",
			`SELECT o.* FROM orders o JOIN customers c ON c.id = o.customer_id WHERE c.org_id = $1`,
			map[string]string{"customers": "unserved/filter customers_pkey", "orders": "unserved/join"}},
		{"join on an indexed foreign key",
			`SELECT o.* FROM orgs g JOIN orders o ON o.org_id = g.id WHERE g.id = $1`,
			map[string]string{"orgs": "served/index_cond orgs_pkey", "orders": "served/index_cond orders_org"}},
		{"ORDER BY … LIMIT served by an index's order", `SELECT * FROM orders ORDER BY id DESC LIMIT 20`,
			map[string]string{"orders": "served/order orders_pkey"}},
		{"ORDER BY … LIMIT that sorts every row", `SELECT * FROM orders ORDER BY created_at DESC LIMIT 20`,
			map[string]string{"orders": "unserved/order"}},
		{"partial index served by its literal",
			`SELECT * FROM orders WHERE status = 'open' ORDER BY created_at LIMIT 5`,
			map[string]string{"orders": "served/partial_index orders_open"}},
		{"partial index unusable by a parameter", `SELECT * FROM orders WHERE status = $1`,
			map[string]string{"orders": "unserved/filter"}},
		{"UPDATE … WHERE with no index", `UPDATE orders SET status = $1 WHERE customer_id = $2`,
			map[string]string{"orders": "unserved/filter"}},
		{"DELETE by primary key", `DELETE FROM orders WHERE id = $1`,
			map[string]string{"orders": "served/index_cond orders_pkey"}},
		{"unfiltered count", `SELECT count(*) FROM orders`,
			map[string]string{"orders": "unfiltered"}},
		{"unfiltered join", `SELECT * FROM orders o JOIN customers c ON c.id = o.customer_id`,
			map[string]string{"orders": "unfiltered", "customers": "unfiltered"}},
		{"INSERT … VALUES reads no table", `INSERT INTO orders (id, status) VALUES ($1, $2)`,
			map[string]string{}},
	}
	shapes := make([]querycapture.Shape, len(cases))
	for i, c := range cases {
		shapes[i] = shape(c.sql)
	}
	queries, findings, err := dbanalyze.AnalyzeQueries(context.Background(), s, info, shapes, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != len(cases) {
		t.Fatalf("%d queries for %d shapes", len(queries), len(cases))
	}
	for i, c := range cases {
		q := queries[i]
		if q.Error != "" {
			t.Errorf("%s: %s", c.name, q.Error)
			continue
		}
		got := access(q)
		if len(got) != len(c.want) {
			t.Errorf("%s: access = %v, want %v", c.name, got, c.want)
			continue
		}
		for table, want := range c.want {
			if got[table] != want {
				t.Errorf("%s: %s = %q, want %q (plan_no_seqscan %+v)", c.name, table, got[table], want, q.PlanNoSeqScan)
			}
		}
		if q.Plan == nil || q.PlanNoSeqScan == nil {
			t.Errorf("%s: plans missing", c.name)
		}
	}

	// Sizes are unknown on a scratch target with no hints: every unserved
	// table is a warning, never a gate, and names the shape's caller.
	unserved := 0
	for _, c := range cases {
		for _, v := range c.want {
			if strings.HasPrefix(v, "unserved") {
				unserved++
			}
		}
	}
	if len(findings) != unserved {
		t.Errorf("%d findings, want %d: %+v", len(findings), unserved, findings)
	}
	for _, f := range findings {
		if f.Rule != dbanalyze.RuleQueryNoUsableIndex || f.Severity != dbanalyze.SeverityWarning ||
			f.File != "internal/db/orders_repo_ext.go" || f.Line != 42 || f.FindingQuery == nil || f.Query == "" || len(f.Callers) != 1 ||
			f.Callers[0].Generated == nil || f.Message == "" || f.Fix == "" {
			t.Errorf("finding %+v", f)
		}
	}
}

// A table's size grades the finding: known and large enough gates, known
// and small is dropped. On a dsn target with statistics the normal plan's
// sequential scan of a large table is its own finding.
func TestAnalyzeQueriesSizesFindings(t *testing.T) {
	s, info := querySession(t)
	ctx := context.Background()
	shapes := []querycapture.Shape{
		shape(`SELECT * FROM orders WHERE customer_id = $1`),
		shape(`SELECT o.* FROM orders o JOIN customers c ON c.id = o.customer_id WHERE c.org_id = $1`),
	}
	cfg := dbanalyze.Config{TableRows: map[string]int64{"orders": 50_000, "customers": 10}}
	_, findings, err := dbanalyze.AnalyzeQueries(ctx, s, info, shapes, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Relation+"/"+f.Severity)
		if f.Rows == nil || f.Rows.Source != dbanalyze.RowsFromHints {
			t.Errorf("%s: rows %+v", f.Relation, f.Rows)
		}
	}
	if strings.Join(got, ",") != "orders/error,orders/error" {
		t.Errorf("findings = %v, want orders gating twice and customers (10 rows) dropped", got)
	}

	// Statistics: the database's own row counts (the scratch database is
	// disposable, so the rows and the ANALYZE simply commit).
	if err := s.Exec(ctx, `INSERT INTO orders (id, customer_id, status) SELECT g, g % 100, 'x' FROM generate_series(1, 5000) g`); err != nil {
		t.Fatal(err)
	}
	if err := s.Exec(ctx, `ANALYZE orders`); err != nil {
		t.Fatal(err)
	}
	dsnInfo := info
	dsnInfo.Kind, dsnInfo.HasStatistics = dbanalyze.TargetDSN, true
	_, findings, err = dbanalyze.AnalyzeQueries(ctx, s, dsnInfo, shapes[:1],
		dbanalyze.Config{SmallTableRows: 100, LargeTableRows: 1000})
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, f := range findings {
		got = append(got, f.Rule+"/"+f.Severity)
		if f.Rows == nil || f.Rows.Source != dbanalyze.RowsFromReltuples || *f.Rows.Value < 4000 {
			t.Errorf("%s: rows %+v", f.Rule, f.Rows)
		}
	}
	if strings.Join(got, ",") != dbanalyze.RuleQueryNoUsableIndex+"/error,"+dbanalyze.RuleQueryLargeSeqScan+"/error" {
		t.Errorf("findings = %v", got)
	}
}

// A shape that cannot be planned is recorded with its error, and the
// transaction survives it for the next shape.
func TestAnalyzeQueriesRecordsShapesThatCannotBePlanned(t *testing.T) {
	s, info := querySession(t)
	shapes := []querycapture.Shape{
		shape(`SELECT * FROM only_my_test_created_this WHERE x = $1`),
		shape(`SELECT $1 + $2`),
		{Fingerprint: "two", SQL: `SELECT 1; DELETE FROM orders`},
		shape(`SELECT * FROM orders WHERE id = $1`),
	}
	queries, findings, err := dbanalyze.AnalyzeQueries(context.Background(), s, info, shapes, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if queries[0].Error == "" || queries[0].SQLState != "42P01" {
		t.Errorf("missing table: %q %q", queries[0].Error, queries[0].SQLState)
	}
	if queries[1].Error == "" {
		t.Error("an ambiguous operator on untyped parameters planned")
	}
	if !strings.Contains(queries[2].Error, "2 statements") {
		t.Errorf("two statements: %q", queries[2].Error)
	}
	if queries[3].Error != "" || access(queries[3])["orders"] != "served/index_cond orders_pkey" {
		t.Errorf("after the failures: %q %v", queries[3].Error, access(queries[3]))
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v", findings)
	}
	doc := dbanalyze.NewDocument()
	doc.Queries = queries
	doc.Finalize()
	if doc.Summary.QueriesScanned != 1 || doc.ExitCode != dbanalyze.ExitOK {
		t.Errorf("summary %+v exit %d", doc.Summary, doc.ExitCode)
	}
}

// The query pass plans on the schema the migration pass committed: a
// concurrent index build runs for real on the disposable database, and the
// plan uses it.
func TestAnalyzeQueriesPlansOnTheSchemaTheMigrationsLeave(t *testing.T) {
	s, info := querySession(t)
	ctx := context.Background()
	migs, _, err := dbanalyze.AnalyzeMigrations(ctx, s, info, []dbanalyze.MigrationFile{
		{File: "db/migrations/2_idx.up.sql", Version: 2, SQL: "CREATE INDEX CONCURRENTLY orders_customer ON orders (customer_id);"},
		{File: "db/migrations/3_col.up.sql", Version: 3, SQL: "ALTER TABLE customers ADD COLUMN email text;"},
	}, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		if !m.Analyzed {
			t.Fatalf("%s: %s", m.File, m.Reason)
		}
	}
	queries, _, err := dbanalyze.AnalyzeQueries(ctx, s, info, []querycapture.Shape{
		shape(`SELECT * FROM orders WHERE customer_id = $1`),
		shape(`SELECT * FROM customers WHERE email = $1`),
	}, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := access(queries[0])["orders"]; got != "served/index_cond orders_customer" {
		t.Errorf("orders = %q", got)
	}
	if queries[1].Error != "" || access(queries[1])["customers"] != "unserved/filter" {
		t.Errorf("the new column: %q %v", queries[1].Error, access(queries[1]))
	}
}

func TestAnalyzeQueriesRefusesPostgresOlderThan16(t *testing.T) {
	_, _, err := dbanalyze.AnalyzeQueries(context.Background(), nil,
		dbanalyze.TargetInfo{ServerVersion: "15.4", ServerVersionNum: 150004}, nil, dbanalyze.Config{})
	if !errors.Is(err, dbanalyze.ErrServerTooOld) || !strings.Contains(err.Error(), "15.4") {
		t.Errorf("err = %v", err)
	}
}

// A captured statement that is not a query is never explained, and the
// EXPLAIN of one that is runs read-only and rolls back: nothing the query
// pass is handed changes the schema the next shape plans against (PR #2212
// review, B1: EXPLAIN accepts utility statements, and the session commits).
func TestAnalyzeQueriesNeverChangesTheDatabase(t *testing.T) {
	s, info := querySession(t)
	ctx := context.Background()
	refused := []string{
		`CREATE TABLE evil AS SELECT 1 AS x`,
		`/* a comment */ ( DROP TABLE orders`,
		`ANALYZE orders`,
		`EXECUTE p (1)`,
		`DECLARE c CURSOR FOR SELECT * FROM orders`,
	}
	var shapes []querycapture.Shape
	for i, sql := range refused {
		shapes = append(shapes, querycapture.Shape{Fingerprint: fmt.Sprintf("refused-%d", i), SQL: sql})
	}
	// Planned, never run: a data change, and a query whose CTE writes.
	shapes = append(shapes,
		shape(`INSERT INTO orders (id, org_id) VALUES ($1, $2)`),
		shape(`WITH w AS (DELETE FROM orgs RETURNING id) SELECT * FROM w`),
		shape(`SELECT * FROM orders WHERE id = $1`),
	)
	queries, _, err := dbanalyze.AnalyzeQueries(ctx, s, info, shapes, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range refused {
		if q := queries[i]; !strings.Contains(q.Error, "not a query") || q.Plan != nil {
			t.Errorf("%q: error %q, plan %v; want it refused unexplained", q.SQL, q.Error, q.Plan)
		}
	}
	for _, q := range queries[len(refused):] {
		if q.Error != "" {
			t.Errorf("%q: %s", q.SQL, q.Error)
		}
	}
	var evil, orders bool
	var rows int
	if err := s.Conn().QueryRowContext(ctx, `SELECT to_regclass('evil') IS NOT NULL, to_regclass('orders') IS NOT NULL,
		(SELECT count(*) FROM orders) + (SELECT count(*) FROM orgs)`).Scan(&evil, &orders, &rows); err != nil {
		t.Fatal(err)
	}
	if evil || !orders || rows != 0 {
		t.Errorf("after the pass: evil=%v orders=%v rows=%d; want the schema and data untouched", evil, orders, rows)
	}
	// The read-only transaction ended with each EXPLAIN: the session is
	// writable again and enable_seqscan is back to on.
	var seq string
	if err := s.Conn().QueryRowContext(ctx, `SELECT current_setting('enable_seqscan')`).Scan(&seq); err != nil || seq != "on" {
		t.Errorf("enable_seqscan = %q (%v), want on", seq, err)
	}
	if _, err := s.Conn().ExecContext(ctx, `CREATE TABLE after_pass (x int)`); err != nil {
		t.Errorf("the session is not writable after the pass: %v", err)
	}
}
