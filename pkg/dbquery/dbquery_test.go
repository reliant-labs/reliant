package dbquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

// newConn opens a connection to a fresh database holding the fixture tables.
func newConn(t *testing.T) *pgx.Conn {
	t.Helper()
	testkit.Slow(t, "boots a real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(context.Background(), `
		CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy');
		CREATE TABLE people (
			id    bigserial PRIMARY KEY,
			name  text NOT NULL,
			mood  mood,
			born  date,
			seen  timestamptz,
			score numeric(10,2),
			tags  text[],
			meta  jsonb,
			big   bigint
		);
		INSERT INTO people (name, mood, born, seen, score, tags, meta, big) VALUES
			('ada',   'happy', '1815-12-10', '2020-01-02 03:04:05+00', 9.50, '{a,b}', '{"k": [1, 2]}', 9007199254740993),
			('grace', 'ok',    '1906-12-09', NULL,                      NULL, NULL,    NULL,            1),
			('linus', NULL,    NULL,         NULL,                      1,    '{}',    '"s"',           NULL);
		CREATE TABLE counted (n int);
		CREATE SEQUENCE seq;`); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return conn
}

func count(t *testing.T, conn *pgx.Conn, table string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantKind(t *testing.T, err error, kind Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("error = %v (kind %q), want kind %q", err, KindOf(err), kind)
	}
	return e
}

func TestRun_TypedResultAndJSON(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn,
		`SELECT name, mood, born, seen, score, tags, meta, big, id FROM people ORDER BY id`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantCols := map[string]string{
		"name": "text", "mood": "mood", "born": "date", "seen": "timestamptz", "score": "numeric",
		"tags": "_text", "meta": "jsonb", "big": "int8", "id": "int8",
	}
	if len(res.Columns) != len(wantCols) {
		t.Fatalf("columns = %+v", res.Columns)
	}
	for _, c := range res.Columns {
		if wantCols[c.Name] != c.PGType {
			t.Errorf("column %s: pg_type %q, want %q", c.Name, c.PGType, wantCols[c.Name])
		}
	}
	if res.RowCount != 3 || len(res.Rows) != 3 || res.Truncated || res.TruncatedBy != "" {
		t.Fatalf("shape: %+v", res)
	}
	// The whole result must marshal; the int8 beyond 2^53 is a string, so a
	// double-parsing reader cannot round it, and the numeric keeps its scale.
	var enc struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(mustJSON(t, res)), &enc); err != nil {
		t.Fatal(err)
	}
	const wantAda = `["ada","happy","1815-12-10","2020-01-02 03:04:05+00",9.50,"{a,b}",{"k":[1,2]},"9007199254740993",1]`
	if got := string(enc.Rows[0]); got != wantAda {
		t.Errorf("ada row:\n got %s\nwant %s", got, wantAda)
	}
	back := struct{ Rows [][]any }{Rows: res.Rows}
	// Row 2: NULLs are JSON null, not "" or "NULL".
	if grace := mustJSON(t, back.Rows[1]); grace != `["grace","ok","1906-12-09",null,null,null,null,1,2]` {
		t.Errorf("grace row: %s", grace)
	}
	if linus := mustJSON(t, back.Rows[2]); linus != `["linus",null,null,null,1.00,"{}","s",null,3]` {
		t.Errorf("linus row: %s", linus)
	}
	if res.DurationMS < 0 || res.Bytes == 0 {
		t.Errorf("duration/bytes: %+v", res)
	}
}

func TestRun_ZeroRowsIsAnEmptyArrayWithColumns(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn, `SELECT id, name FROM people WHERE false`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows); got != `[]` {
		t.Errorf("rows = %s, want []", got)
	}
	if len(res.Columns) != 2 || res.RowCount != 0 || res.Truncated {
		t.Errorf("%+v", res)
	}
}

func TestRun_ReadsAnInformationalStatement(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`SHOW statement_timeout`,
		`TABLE people`,
		`VALUES (1), (2)`,
		`EXPLAIN SELECT * FROM people`,
		`EXPLAIN (ANALYZE, FORMAT JSON) SELECT * FROM people`,
		`WITH p AS (SELECT * FROM people) SELECT count(*) FROM p`,
	} {
		if _, err := Run(context.Background(), conn, sql, Options{}); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

// The read-only replication views run against a real server: the guard denies
// the replication functions that change state by name, not the whole
// pg_replication_ family, so a replication-lag panel works.
func TestRun_ReadsTheReplicationViews(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`SELECT slot_name, active, pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) AS lag FROM pg_replication_slots`,
		`SELECT * FROM pg_replication_origin_status`,
		`SELECT * FROM pg_stat_replication`,
		`SELECT * FROM pg_stat_replication_slots`,
	} {
		if _, err := Run(context.Background(), conn, sql, Options{}); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	_, err := Run(context.Background(), conn, `SELECT pg_replication_origin_create('o')`, Options{})
	if e := wantKind(t, err, KindRefused); e.Reason != "denied_function" {
		t.Errorf("pg_replication_origin_create: reason %q, want denied_function", e.Reason)
	}
}

// The timeouts apply to the query's own transaction only, and are visible to
// it, so what the server enforces is what we set.
func TestRun_SetsTransactionLimitsAndReadOnly(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn,
		`SELECT current_setting('transaction_read_only') AS ro, current_setting('transaction_isolation') AS iso,
		        current_setting('statement_timeout') AS st, current_setting('lock_timeout') AS lt`,
		Options{Timeout: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows[0]); got != `["on","read committed","7s","1s"]` {
		t.Errorf("settings = %s", got)
	}
	// And nothing leaked onto the connection.
	var st string
	if err := conn.QueryRow(context.Background(), `SHOW statement_timeout`).Scan(&st); err != nil || st != "0" {
		t.Errorf("connection statement_timeout = %q, %v; want the server default", st, err)
	}
}

// --- read-only enforcement -------------------------------------------------

func TestRun_GuardRefusesWrites(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`INSERT INTO counted VALUES (1)`,
		`UPDATE people SET name = 'x'`,
		`DELETE FROM people`,
		`DROP TABLE people`,
		`TRUNCATE people`,
		`CREATE TABLE t (a int)`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{})
		e := wantKind(t, err, KindRefused)
		if e.Reason != "not_read_only" {
			t.Errorf("%s: reason %q", sql, e.Reason)
		}
	}
	if n := count(t, conn, "people"); n != 3 {
		t.Fatalf("people rows = %d after refused writes", n)
	}
}

// The guard is the first line, not the boundary: a SELECT-led statement that
// writes passes it, and the read-only transaction is what refuses it.
func TestRun_ReadOnlyTransactionRefusesWhatTheGuardLetsThrough(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`SELECT nextval('seq')`,
		`SELECT setval('seq', 100)`,
		`WITH ins AS (INSERT INTO counted VALUES (1) RETURNING n) SELECT * FROM ins`,
		`WITH d AS (DELETE FROM people RETURNING id) SELECT * FROM d`,
		`WITH u AS (UPDATE people SET name = 'x' RETURNING id) SELECT * FROM u`,
	} {
		if _, err := Check(sql); err != nil {
			t.Fatalf("premise: the guard should let %q through, got %v", sql, err)
		}
		_, err := Run(context.Background(), conn, sql, Options{})
		wantKind(t, err, KindReadOnly)
	}
	if n := count(t, conn, "people"); n != 3 {
		t.Fatalf("people rows = %d", n)
	}
	if n := count(t, conn, "counted"); n != 0 {
		t.Fatalf("counted rows = %d", n)
	}
	var last int64
	var called bool
	if err := conn.QueryRow(context.Background(), `SELECT last_value, is_called FROM seq`).Scan(&last, &called); err != nil || called {
		t.Fatalf("sequence moved: last=%d called=%v err=%v", last, called, err)
	}
}

// With the guard switched off entirely, the transaction alone refuses every
// write the design names.
func TestRun_ReadOnlyTransactionAloneRefusesEveryWrite(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`INSERT INTO counted VALUES (1)`,
		`UPDATE people SET name = 'x'`,
		`DELETE FROM people`,
		`TRUNCATE people`,
		`CREATE TABLE t (a int)`,
		`DROP TABLE people`,
		`SELECT nextval('seq')`,
		`ALTER TABLE people ADD COLUMN z int`,
		`CREATE SEQUENCE s2`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{skipGuard: true})
		wantKind(t, err, KindReadOnly)
	}
	if n := count(t, conn, "people"); n != 3 {
		t.Fatalf("people rows = %d", n)
	}
}

// A statement cannot escape the read-only transaction by issuing its own
// SET: the extended protocol takes one statement, so nothing can follow a
// COMMIT (which is why it is not in the list: it ends the transaction and
// there is nothing left to run), and the transaction cannot be flipped to
// read-write.
func TestRun_CannotLeaveTheTransaction(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`SET TRANSACTION READ WRITE`,
		`SET LOCAL transaction_read_only = off`,
		`SELECT set_config('transaction_read_only', 'off', true)`,
		`ROLLBACK; INSERT INTO counted VALUES (1)`,
		`SELECT 1; INSERT INTO counted VALUES (1)`,
		`END; INSERT INTO counted VALUES (1)`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{skipGuard: true})
		if err == nil {
			// set_config on transaction_read_only is the one a server may accept as
			// a no-op; the proof is the follow-up write below failing.
			if !strings.Contains(sql, "set_config") {
				t.Errorf("%q succeeded", sql)
			}
		}
	}
	if _, err := Run(context.Background(), conn,
		`WITH i AS (INSERT INTO counted VALUES (1) RETURNING n) SELECT * FROM i`, Options{}); KindOf(err) != KindReadOnly {
		t.Fatalf("a write after the escape attempts: %v", err)
	}
	if n := count(t, conn, "counted"); n != 0 {
		t.Fatalf("counted rows = %d", n)
	}
}

func TestRun_MultipleStatementsRefused(t *testing.T) {
	conn := newConn(t)
	// By the guard.
	_, err := Run(context.Background(), conn, `SELECT 1; INSERT INTO counted VALUES (1)`, Options{})
	if e := wantKind(t, err, KindRefused); e.Reason != "multiple_statements" {
		t.Errorf("guard reason = %q", e.Reason)
	}
	_, err = Run(context.Background(), conn, `SELECT 1; SELECT 2`, Options{})
	if e := wantKind(t, err, KindRefused); e.Reason != "multiple_statements" {
		t.Errorf("guard reason = %q", e.Reason)
	}
	// By the server, with the guard off: the extended protocol takes one.
	_, err = Run(context.Background(), conn, `SELECT 1; SELECT 2`, Options{skipGuard: true})
	if e := wantKind(t, err, KindRefused); e.Reason != "multiple_statements" {
		t.Errorf("server reason = %q (%v)", e.Reason, err)
	}
	_, err = Run(context.Background(), conn, `SELECT 1; INSERT INTO counted VALUES (1)`, Options{skipGuard: true})
	wantKind(t, err, KindRefused)
	if n := count(t, conn, "counted"); n != 0 {
		t.Fatalf("counted rows = %d", n)
	}
}

func TestRun_SQLErrorsAreTypedAndTheConnectionStaysUsable(t *testing.T) {
	conn := newConn(t)
	_, err := Run(context.Background(), conn, `SELECT nope FROM people`, Options{})
	if e := wantKind(t, err, KindSQL); e.Reason != "42703" || !strings.Contains(e.Message, "nope") {
		t.Errorf("%+v", e)
	}
	_, err = Run(context.Background(), conn, `SELEC 1`, Options{})
	wantKind(t, err, KindRefused) // the guard: SELEC is not a read statement
	_, err = Run(context.Background(), conn, `SELECT * FROM`, Options{})
	wantKind(t, err, KindSQL)
	if _, err := Run(context.Background(), conn, `SELECT 1`, Options{}); err != nil {
		t.Fatalf("connection unusable after errors: %v", err)
	}
}

// --- timeout ---------------------------------------------------------------

func TestRun_StatementTimeoutIsATypedError(t *testing.T) {
	conn := newConn(t)
	start := time.Now()
	_, err := Run(context.Background(), conn, `SELECT pg_sleep(30)`, Options{Timeout: 200 * time.Millisecond})
	e := wantKind(t, err, KindTimeout)
	if e.Reason != "statement_timeout" {
		t.Errorf("reason = %q", e.Reason)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s: the server did not enforce the timeout", d)
	}
	if _, err := Run(context.Background(), conn, `SELECT 1`, Options{}); err != nil {
		t.Fatalf("connection unusable after a timeout: %v", err)
	}
}

// A statement that lifts its own timeout (set_config is allowed in a
// read-only transaction, and only the guard refuses it) is still stopped by the
// client-side deadline.
func TestRun_ClientDeadlineBacksUpTheServerTimeout(t *testing.T) {
	conn := newConn(t)
	old := ctxGrace
	ctxGrace = 300 * time.Millisecond
	t.Cleanup(func() { ctxGrace = old })

	start := time.Now()
	// skipGuard: the guard refuses set_config; this proves the deadline holds
	// for a statement that got past it anyway.
	_, err := Run(context.Background(), conn,
		`SELECT set_config('statement_timeout', '0', true), pg_sleep(30)`, Options{Timeout: 200 * time.Millisecond, skipGuard: true})
	wantKind(t, err, KindTimeout)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s: the client deadline did not fire", d)
	}
}

func TestRun_CallerCancellation(t *testing.T) {
	conn := newConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Run(ctx, conn, `SELECT pg_sleep(30)`, Options{})
	if k := KindOf(err); k != KindCanceled && k != KindUnavailable {
		t.Fatalf("kind = %q (%v)", k, err)
	}
}

// --- row cap ---------------------------------------------------------------

func TestRun_RowCapTruncatesAndSaysSo(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn, `SELECT g FROM generate_series(1, 1000) g`, Options{MaxRows: 25})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != 25 || len(res.Rows) != 25 || !res.Truncated || res.TruncatedBy != "rows" {
		t.Fatalf("%d rows, truncated=%v by %q", res.RowCount, res.Truncated, res.TruncatedBy)
	}
	if got := mustJSON(t, res.Rows[24]); got != `[25]` {
		t.Errorf("last kept row = %s", got)
	}
	if enc := mustJSON(t, res); !strings.Contains(enc, `"truncated":true`) || !strings.Contains(enc, `"truncated_by":"rows"`) {
		t.Errorf("the encoding must say it was cut: %s", enc)
	}
}

func TestRun_RowCapBoundaries(t *testing.T) {
	conn := newConn(t)
	for _, tc := range []struct {
		have, cap int
		truncated bool
	}{{10, 10, false}, {11, 10, true}, {9, 10, false}, {1, 1, false}, {2, 1, true}} {
		res, err := Run(context.Background(), conn,
			fmt.Sprintf(`SELECT g FROM generate_series(1, %d) g`, tc.have), Options{MaxRows: tc.cap})
		if err != nil {
			t.Fatal(err)
		}
		if res.Truncated != tc.truncated || res.RowCount != min(tc.have, tc.cap) {
			t.Errorf("have %d cap %d: rows %d truncated %v", tc.have, tc.cap, res.RowCount, res.Truncated)
		}
	}
}

func TestRun_DefaultRowCap(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn, `SELECT g FROM generate_series(1, 500) g`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != DefaultMaxRows || !res.Truncated {
		t.Fatalf("%d rows, truncated=%v", res.RowCount, res.Truncated)
	}
}

// A cap hit while a long query is still producing rows does not wait for the
// rest of it: the work after the cap is bounded by the statement timeout, and
// the rows we hold are returned.
func TestRun_RowCapOnASlowTail(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn,
		`SELECT g, CASE WHEN g > 3 THEN pg_sleep(30)::text END FROM generate_series(1, 10) g`,
		Options{MaxRows: 2, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != 2 || !res.Truncated {
		t.Fatalf("%+v", res)
	}
	if _, err := Run(context.Background(), conn, `SELECT 1`, Options{}); err != nil {
		t.Fatalf("connection unusable after a capped slow query: %v", err)
	}
}

func TestRun_ByteCapTruncates(t *testing.T) {
	conn := newConn(t)
	// 3 KiB cells: 4 MiB / 3 KiB is ~1300 rows, well under the 10,000 row cap.
	res, err := Run(context.Background(), conn,
		`SELECT repeat('x', 3000) FROM generate_series(1, 5000)`, Options{MaxRows: MaxRowsLimit})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.TruncatedBy != "bytes" || res.Bytes > MaxResponseBytes || res.RowCount >= 5000 || res.RowCount < 1000 {
		t.Fatalf("rows=%d bytes=%d truncated=%v by %q", res.RowCount, res.Bytes, res.Truncated, res.TruncatedBy)
	}
}

func TestRun_CellTruncation(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn,
		`SELECT repeat('a', 10000) AS big, repeat('b', 100) AS small, repeat('é', 3000) AS wide`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	big, small, wide := res.Rows[0][0].(string), res.Rows[0][1].(string), res.Rows[0][2].(string)
	if !strings.HasPrefix(big, strings.Repeat("a", MaxCellBytes)) || !strings.Contains(big, "truncated: 4096 of 10000 bytes shown") {
		t.Errorf("big = %.40s…%s", big, big[max(0, len(big)-50):])
	}
	if small != strings.Repeat("b", 100) {
		t.Error("a small cell must not be touched")
	}
	if !strings.Contains(wide, "of 6000 bytes shown") || strings.ContainsRune(wide, '\uFFFD') {
		t.Errorf("wide cut mid-rune or unmarked: %.30s…", wide)
	}
	if res.CellsTruncated != 2 || res.Truncated {
		t.Errorf("cells_truncated=%d truncated=%v: a cut cell is not a cut result", res.CellsTruncated, res.Truncated)
	}
}

// --- explain ---------------------------------------------------------------

func TestRun_ExplainAnalyzeOnlyOverSelect(t *testing.T) {
	conn := newConn(t)
	if _, err := Run(context.Background(), conn, `EXPLAIN ANALYZE SELECT * FROM people`, Options{}); err != nil {
		t.Fatalf("EXPLAIN ANALYZE SELECT: %v", err)
	}
	for _, sql := range []string{
		`EXPLAIN ANALYZE INSERT INTO counted VALUES (1)`,
		`EXPLAIN ANALYZE WITH d AS (DELETE FROM people RETURNING id) SELECT * FROM d`,
		`EXPLAIN (ANALYZE) TABLE people`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{})
		wantKind(t, err, KindRefused)
	}
	// With the guard off the transaction is still the boundary: ANALYZE would
	// execute the INSERT, and the read-only transaction refuses it.
	_, err := Run(context.Background(), conn, `EXPLAIN ANALYZE INSERT INTO counted VALUES (1)`, Options{skipGuard: true})
	wantKind(t, err, KindReadOnly)
	if n := count(t, conn, "counted"); n != 0 {
		t.Fatalf("counted rows = %d", n)
	}
}

// --- permissions -----------------------------------------------------------

// The role is the real boundary for what may be READ: a column it has no
// SELECT on cannot be read through any expression.
func TestRun_ColumnPrivilegeIsEnforcedByPostgres(t *testing.T) {
	conn := newConn(t)
	ctx := context.Background()
	// Roles are cluster-wide, and the server is shared across tests and runs:
	// a unique name, dropped on the way out.
	role := fmt.Sprintf("ro_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE ROLE %[1]s NOLOGIN;
		GRANT USAGE ON SCHEMA public TO %[1]s;
		GRANT SELECT (id, name) ON people TO %[1]s;`, role)); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `RESET ROLE`)
		_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %[1]s; DROP ROLE %[1]s`, role))
	})
	if _, err := conn.Exec(ctx, `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, conn, `SELECT id, name FROM people`, Options{}); err != nil {
		t.Fatalf("granted columns: %v", err)
	}
	for _, sql := range []string{
		`SELECT meta FROM people`,
		`SELECT upper(meta::text) FROM people`,
		`SELECT * FROM people`,
		`SELECT name FROM people WHERE meta IS NOT NULL`,
		`SELECT count(*) FROM people GROUP BY big`,
	} {
		_, err := Run(ctx, conn, sql, Options{})
		wantKind(t, err, KindPermission)
	}
}

// --- audit -----------------------------------------------------------------

type recorder struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (r *recorder) Audit(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

func TestRun_AuditSeesEveryAttemptPastValidation(t *testing.T) {
	conn := newConn(t)
	rec := &recorder{}
	opts := Options{Auditor: rec, MaxRows: 5}

	if _, err := Run(context.Background(), conn, `SELECT g FROM generate_series(1, 9) g`, opts); err != nil {
		t.Fatal(err)
	}
	_, _ = Run(context.Background(), conn, `DELETE FROM people`, opts)    // refused by the guard
	_, _ = Run(context.Background(), conn, `SELECT nextval('seq')`, opts) // refused by the transaction
	_, _ = Run(context.Background(), conn, `SELECT pg_sleep(9)`, Options{Auditor: rec, Timeout: 100 * time.Millisecond})
	_, _ = Run(context.Background(), conn, `SELECT 1`, Options{Auditor: rec, MaxRows: MaxRowsLimit + 1}) // refused before it could run: not an attempt

	if len(rec.events) != 4 {
		t.Fatalf("%d audit events, want 4: %+v", len(rec.events), rec.events)
	}
	ok, guard, tx, timeout := rec.events[0], rec.events[1], rec.events[2], rec.events[3]
	if ok.Outcome != "ok" || ok.Err != nil || ok.RowCount != 5 || !ok.Truncated || ok.Leader != "SELECT" || ok.MaxRows != 5 || ok.Bytes == 0 {
		t.Errorf("ok event: %+v", ok)
	}
	if ok.SQL != `SELECT g FROM generate_series(1, 9) g` || len(ok.SQLSHA256) != 64 || ok.StartedAt.IsZero() || ok.Duration <= 0 {
		t.Errorf("ok event identity: %+v", ok)
	}
	if guard.Outcome != string(KindRefused) || guard.SQL != `DELETE FROM people` || guard.Err == nil {
		t.Errorf("guard event: %+v", guard)
	}
	if tx.Outcome != string(KindReadOnly) || tx.Leader != "SELECT" {
		t.Errorf("tx event: %+v", tx)
	}
	if timeout.Outcome != string(KindTimeout) {
		t.Errorf("timeout event: %+v", timeout)
	}
	if ok.SQLSHA256 == guard.SQLSHA256 {
		t.Error("distinct statements, same hash")
	}
}

func TestRun_AuditFailureWithholdsTheRows(t *testing.T) {
	conn := newConn(t)
	boom := errors.New("audit store down")
	res, err := Run(context.Background(), conn, `SELECT 1`, Options{Auditor: &recorder{err: boom}})
	if res != nil {
		t.Errorf("rows returned though the query was not recorded: %+v", res)
	}
	if e := wantKind(t, err, KindAudit); !errors.Is(e, boom) {
		t.Errorf("cause lost: %v", e)
	}
	// An attempt that already failed keeps its own error.
	_, err = Run(context.Background(), conn, `DELETE FROM people`, Options{Auditor: &recorder{err: boom}})
	wantKind(t, err, KindRefused)
}

func TestRun_AuditRunsEvenWhenTheCallerCanceled(t *testing.T) {
	conn := newConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	var sawLive bool
	aud := AuditFunc(func(actx context.Context, _ Event) error {
		sawLive = actx.Err() == nil
		return nil
	})
	time.AfterFunc(150*time.Millisecond, cancel)
	_, _ = Run(ctx, conn, `SELECT pg_sleep(30)`, Options{Auditor: aud})
	if !sawLive {
		t.Error("the audit context died with the caller's: a canceled query would go unrecorded")
	}
}

// --- isolation, lock timeout, arguments ------------------------------------

func TestRun_IsReadCommittedAndReadOnly(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn, `SHOW transaction_isolation`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows); got != `[["read committed"]]` {
		t.Errorf("transaction_isolation = %s", got)
	}
	res, err = Run(context.Background(), conn, `SHOW transaction_read_only`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows); got != `[["on"]]` {
		t.Errorf("transaction_read_only = %s", got)
	}
}

// A writer holding ACCESS EXCLUSIVE must cost a query its lock timeout and no
// more: the query gives up instead of queueing behind the writer.
func TestRun_LockTimeoutFiresBehindAnAccessExclusiveLock(t *testing.T) {
	conn := newConn(t)
	ctx := context.Background()
	holder, err := pgx.ConnectConfig(ctx, conn.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close(ctx) })
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `LOCK TABLE people IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = Run(ctx, conn, `SELECT count(*) FROM people`, Options{LockTimeout: 150 * time.Millisecond, Timeout: 20 * time.Second})
	e := wantKind(t, err, KindTimeout)
	if e.Reason != "lock_timeout" {
		t.Errorf("reason = %q, want lock_timeout", e.Reason)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s: the lock timeout did not fire", took)
	}

	// The lock timeout is what the transaction runs under.
	res, err := Run(ctx, conn, `SELECT current_setting('lock_timeout'), current_setting('idle_in_transaction_session_timeout')`,
		Options{LockTimeout: 3 * time.Second, IdleTimeout: 4 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows[0]); got != `["3s","4s"]` {
		t.Errorf("timeouts = %s", got)
	}
}

func TestRun_ArgsBind(t *testing.T) {
	conn := newConn(t)
	ctx := context.Background()
	from := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	res, err := Run(ctx, conn,
		`SELECT name, $3::int4 AS step FROM people WHERE seen >= $1::timestamptz AND seen < $2::timestamptz OR name = $4 ORDER BY id`,
		Options{Args: []any{from, from.Add(24 * time.Hour), 60, "grace"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows); got != `[["ada",60],["grace",60]]` {
		t.Errorf("rows = %s", got)
	}

	// Values are bound, never spliced.
	res, err = Run(ctx, conn, `SELECT count(*) FROM people WHERE name = $1`, Options{Args: []any{"x' OR true --"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, res.Rows); got != `[[0]]` {
		t.Errorf("injection-shaped arg matched rows: %s", got)
	}
}

// Only the parameters the statement references are bound, so a caller may
// hand every parameter it has to a statement that uses a few.
func TestRun_BindsOnlyWhatTheStatementReferences(t *testing.T) {
	conn := newConn(t)
	rec := &recorder{}
	args := []any{time.Now(), time.Now(), 60}
	for _, sql := range []string{`SELECT 1`, `SELECT $1::timestamptz IS NOT NULL`, `SELECT $1::timestamptz IS NOT NULL, $2::timestamptz IS NOT NULL, $3::int4`} {
		if _, err := Run(context.Background(), conn, sql, Options{Args: args, Auditor: rec}); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	for i, want := range []int{0, 1, 3} {
		if got := len(rec.events[i].Args); got != want {
			t.Errorf("event %d carries %d args, want %d", i, got, want)
		}
	}
	// A statement that skips $1 cannot be typed by Postgres: that is its error.
	_, err := Run(context.Background(), conn, `SELECT $2::int4`, Options{Args: args})
	wantKind(t, err, KindSQL)
}

func TestRun_RefusesAReferenceBeyondTheArgs(t *testing.T) {
	conn := newConn(t)
	rec := &recorder{}
	_, err := Run(context.Background(), conn, `SELECT $1::int4, $2::int4, $4::int4`, Options{Args: []any{1, 2, 3}, Auditor: rec})
	if e := wantKind(t, err, KindRefused); e.Reason != "missing_args" {
		t.Errorf("reason = %q", e.Reason)
	}
	if len(rec.events) != 1 || rec.events[0].Outcome != string(KindRefused) {
		t.Errorf("the refused attempt must be audited: %+v", rec.events)
	}
	// A $4 that only appears inside a string is not a reference.
	if _, err := Run(context.Background(), conn, `SELECT '$4', $1::int4`, Options{Args: []any{1}}); err != nil {
		t.Errorf("a $4 in a string refused: %v", err)
	}
}

func TestRun_MaxBytesCapsBelowTheResponseCap(t *testing.T) {
	conn := newConn(t)
	res, err := Run(context.Background(), conn, `SELECT repeat('x', 100) FROM generate_series(1, 50)`, Options{MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.TruncatedBy != "bytes" || res.Bytes > 1000 || res.RowCount == 0 || res.RowCount >= 50 {
		t.Errorf("result = rows %d bytes %d truncated %v by %q", res.RowCount, res.Bytes, res.Truncated, res.TruncatedBy)
	}
}

func TestRun_AuditEventCarriesTheArgsAsValues(t *testing.T) {
	conn := newConn(t)
	rec := &recorder{}
	sql := `SELECT $1::text`
	if _, err := Run(context.Background(), conn, sql, Options{Args: []any{"v"}, Auditor: rec}); err != nil {
		t.Fatal(err)
	}
	ev := rec.events[0]
	if ev.SQL != sql || len(ev.Args) != 1 || ev.Args[0] != "v" {
		t.Errorf("event = %+v", ev)
	}
}

// The guard's refusals, end to end: each spelling is a way a statement was
// once found to raise work_mem past the guard. Run refuses it, and a
// statement that runs leaves work_mem as the transaction had it.
func TestRun_SessionSettingsCannotBeChangedPastTheGuard(t *testing.T) {
	conn := newConn(t)
	for _, sql := range []string{
		`SELECT name'\' AS x, set_config('work_mem','1GB',true) AS y, '\' AS z`,
		"SELECT E'x'\n'\\' AS a, ' AS b, set_config('work_mem','1GB',true) AS c, 'q' AS d",
		`SELECT ts_rewrite('a'::tsquery, 'SELECT set_config(''work_mem'',''3GB'',true)::tsquery, ''b''::tsquery')`,
		`SELECT * FROM ts_stat('SELECT set_config(''work_mem'',''2GB'',true)::tsvector')`,
		`SELECT query_to_xml('SELECT set_config(''work_mem'',''2GB'',true)', true, false, '')`,
		`SELECT "pg_catalog"."set_config"('work_mem', '1GB', true)`,
		`SELECT (x)."ts_stat" FROM (SELECT 'SELECT pg_catalog.set_config(''work_mem'',''5GB'',true)::tsvector'::text AS x) t`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{})
		if e := wantKind(t, err, KindRefused); e.Reason != "denied_function" {
			t.Errorf("%q refused as %q, want denied_function", sql, e.Reason)
		}
	}
	for _, sql := range []string{
		`SELECT U&"set!005fconfig" UESCAPE '!' ('work_mem','1GB',true) AS y, current_setting('work_mem') AS w`,
		`SELECT pg_catalog.U&"set!005fconfig" UESCAPE '!' ('work_mem','1GB',true)`,
		`SELECT * FROM U&"ts!005fstat" UESCAPE '!' ('SELECT pg_catalog.set_config(''work_mem'',''2GB'',true)::tsvector')`,
		`SELECT U&"pg!005fadvisory!005flock" UESCAPE '!' (1)`,
	} {
		_, err := Run(context.Background(), conn, sql, Options{})
		if e := wantKind(t, err, KindRefused); e.Reason != "unicode_identifier" {
			t.Errorf("%q refused as %q, want unicode_identifier", sql, e.Reason)
		}
	}
	// What the guard accepts runs with the transaction's own work_mem.
	res, err := Run(context.Background(), conn, `SELECT current_setting('work_mem') AS w, 'set_config(' AS s, name'x' AS n`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var def string
	if err := conn.QueryRow(context.Background(), `SHOW work_mem`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0][0]; got != def {
		t.Fatalf("work_mem = %v, want the server's %s", got, def)
	}
}

// A database or role default can switch standard_conforming_strings off, and
// then '\' escapes a quote: the server would read a different statement than
// the guard judged. Run pins it on for its transaction.
func TestRun_StringsAreReadAsTheGuardReadsThem(t *testing.T) {
	conn := newConn(t)
	if _, err := conn.Exec(context.Background(), `SET standard_conforming_strings = off`); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), conn, `SELECT 'a\' AS x, 1 AS y`, Options{})
	if err != nil {
		t.Fatalf("a plain string ending in a backslash: %v", err)
	}
	if got := mustJSON(t, res.Rows[0]); got != `["a\\",1]` {
		t.Fatalf("row = %s", got)
	}
}

// The limits Run sets go through pg_catalog.set_config, so a search_path that
// puts a schema ahead of pg_catalog (a database or role default can) does not
// let a same-named function there take its place.
func TestRun_SetsItsLimitsThroughPgCatalogWhateverTheSearchPath(t *testing.T) {
	conn := newConn(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `CREATE FUNCTION public.set_config(text, text, boolean) RETURNS text
		LANGUAGE sql AS $$ SELECT 'shadowed'::text $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET search_path = public, pg_catalog`); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, conn, `SELECT pg_catalog.current_setting('statement_timeout') AS st`, Options{Timeout: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0][0]; got != "7s" {
		t.Fatalf("statement_timeout = %v: the shadowing set_config was called instead of pg_catalog's", got)
	}
}
