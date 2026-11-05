package dbquery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestCheck_Accepts(t *testing.T) {
	t.Parallel()
	for sql, leader := range map[string]string{
		"SELECT 1":                     "SELECT",
		"select 1;":                    "SELECT",
		"  \n\t SELECT 1 ;  ;  ":       "SELECT",
		"/* lead */ SELECT 1 -- trail": "SELECT",
		"-- note\nSELECT 1":            "SELECT",
		"/* a /* nested */ still comment */ SELECT 1":      "SELECT",
		"(SELECT 1) UNION (SELECT 2)":                      "SELECT",
		"WITH x AS (SELECT 1) SELECT * FROM x":             "WITH",
		"TABLE users":                                      "TABLE",
		"VALUES (1), (2)":                                  "VALUES",
		"SHOW statement_timeout":                           "SHOW",
		"EXPLAIN SELECT 1":                                 "EXPLAIN",
		"EXPLAIN (FORMAT JSON) SELECT 1":                   "EXPLAIN",
		"EXPLAIN ANALYZE SELECT 1":                         "EXPLAIN",
		"EXPLAIN (ANALYZE, BUFFERS) SELECT 1":              "EXPLAIN",
		"EXPLAIN (ANALYZE true) (SELECT 1)":                "EXPLAIN",
		"EXPLAIN ANALYZE VERBOSE SELECT 1":                 "EXPLAIN",
		"EXPLAIN VERBOSE WITH x AS (SELECT 1) SELECT 1":    "EXPLAIN",
		"EXPLAIN WITH x AS (SELECT 1) SELECT * FROM x":     "EXPLAIN",
		"SELECT ';' , 'it''s ; fine'":                      "SELECT",
		`SELECT ";" FROM "a;b"`:                            "SELECT",
		"SELECT E'a\\';b'":                                 "SELECT",
		"SELECT $$a;b$$, $tag$ ; $$ ; $tag$":               "SELECT",
		"SELECT $1, $2":                                    "SELECT",
		"SELECT 'a' -- ; DROP TABLE x":                     "SELECT",
		"SELECT 1 /* ; DROP TABLE x */":                    "SELECT",
		"SELECT 'é;ü', 1":                                  "SELECT",
		"select * from public.t where a = 'delete from x'": "SELECT",
	} {
		sql, leader := sql, leader
		t.Run(sql, func(t *testing.T) {
			t.Parallel()
			got, err := Check(sql)
			if err != nil || got != leader {
				t.Fatalf("Check = %q, %v; want %q", got, err, leader)
			}
		})
	}
}

func TestCheck_Refuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ sql, reason string }{
		{"", "empty"},
		{" ;; -- nothing\n", "empty"},
		{"/* only a comment */", "empty"},
		{"SELECT 1; SELECT 2", "multiple_statements"},
		{"SELECT 1;\n-- c\nDROP TABLE t", "multiple_statements"},
		{"; SELECT 1; SELECT 2", "multiple_statements"},
		{"SELECT 1; /* c */ SELECT 2;", "multiple_statements"},
		{"INSERT INTO t VALUES (1)", "not_read_only"},
		{"update t set a = 1", "not_read_only"},
		{"DELETE FROM t", "not_read_only"},
		{"DROP TABLE t", "not_read_only"},
		{"TRUNCATE t", "not_read_only"},
		{"CREATE TABLE t (a int)", "not_read_only"},
		{"ALTER TABLE t ADD b int", "not_read_only"},
		{"GRANT SELECT ON t TO x", "not_read_only"},
		{"COPY t TO PROGRAM 'x'", "not_read_only"},
		{"CALL p()", "not_read_only"},
		{"DO $$ begin end $$", "not_read_only"},
		{"SET ROLE postgres", "session_state"},
		{"BEGIN", "not_read_only"},
		{"COMMIT", "not_read_only"},
		{"LOCK t", "not_read_only"},
		{"VACUUM", "not_read_only"},
		{"EXECUTE p", "not_read_only"},
		{"(INSERT INTO t VALUES (1))", "not_read_only"},
		{"1", "not_read_only"},
		{"'SELECT 1'", "not_read_only"},
		{"EXPLAIN ANALYZE INSERT INTO t VALUES (1)", "not_read_only"},
		{"EXPLAIN INSERT INTO t VALUES (1)", "not_read_only"},
		{"EXPLAIN (ANALYZE) DELETE FROM t", "not_read_only"},
		{"EXPLAIN", "not_read_only"},
		{"EXPLAIN ANALYZE WITH x AS (SELECT 1) SELECT * FROM x", "explain_analyze"},
		{"EXPLAIN (ANALYZE) TABLE t", "explain_analyze"},
		{"EXPLAIN (ANALYZE, VERBOSE) VALUES (1)", "explain_analyze"},
		{"EXPLAIN ANALYSE WITH x AS (SELECT 1) SELECT 1", "explain_analyze"},
		{"SELECT 'unterminated", "unterminated"},
		{`SELECT "unterminated`, "unterminated"},
		{"SELECT $$unterminated", "unterminated"},
		{"SELECT 1 /* unterminated", "unterminated"},
		{"SELECT 1\x00", "invalid_text"},
		{"SELECT '\xff'", "invalid_text"},
		{"SELECT '" + strings.Repeat("a", MaxSQLBytes) + "'", "sql_too_large"},
		{"SET statement_timeout = 0", "session_state"},
		{"set work_mem = '1GB'", "session_state"},
		{"RESET ALL", "session_state"},
		{"/* x */ SET LOCAL work_mem = '1GB'", "session_state"},
	} {
		tc := tc
		name := tc.sql
		if len(name) > 60 {
			name = name[:60]
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Check(tc.sql)
			var e *Error
			if !errors.As(err, &e) || e.Kind != KindRefused || e.Reason != tc.reason {
				t.Fatalf("Check(%q) = %v; want refused/%s", name, err, tc.reason)
			}
		})
	}
}

// A word that merely contains a keyword, or a literal that spells one, is not
// the keyword: the guard reads tokens, not substrings.
func TestCheck_ReadsTokensNotSubstrings(t *testing.T) {
	t.Parallel()
	for _, sql := range []string{
		"SELECT 'EXPLAIN ANALYZE INSERT'",
		"SELECT insert_count, update_at FROM t",
		`SELECT "EXPLAIN" FROM t`,
	} {
		if _, err := Check(sql); err != nil {
			t.Errorf("Check(%q) = %v", sql, err)
		}
	}
	// An E-string's backslash-escaped quote does not end the literal, so the
	// ';' after it is still inside it.
	if _, err := Check(`SELECT E'x\'; DROP TABLE t; --'`); err != nil {
		t.Errorf("E-string with an escaped quote: %v", err)
	}
	// A plain string has no backslash escapes: that same text ends the literal
	// at the second quote and the ';' is real.
	if _, err := Check(`SELECT 'x\'; DROP TABLE t; --'`); KindOf(err) != KindRefused {
		t.Errorf("plain string: want refusal, got %v", err)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	pg := func(code, msg string) error {
		return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code, Message: msg})
	}
	for _, tc := range []struct {
		name   string
		err    error
		kind   Kind
		reason string
	}{
		{"read-only tx", pg("25006", "cannot execute INSERT in a read-only transaction"), KindReadOnly, ""},
		{"permission", pg("42501", "permission denied for table t"), KindPermission, ""},
		{"statement timeout", pg("57014", "canceling statement due to statement timeout"), KindTimeout, "statement_timeout"},
		{"user cancel", pg("57014", "canceling statement due to user request"), KindCanceled, ""},
		{"lock timeout", pg("55P03", "canceling statement due to lock timeout"), KindTimeout, "lock_timeout"},
		{"multiple commands", pg("42601", "cannot insert multiple commands into a prepared statement"), KindRefused, "multiple_statements"},
		{"syntax", pg("42601", "syntax error at or near \"x\""), KindSQL, "42601"},
		{"undefined column", pg("42703", "column \"x\" does not exist"), KindSQL, "42703"},
		{"admin shutdown", pg("57P01", "terminating connection due to administrator command"), KindUnavailable, ""},
		{"connection failure", pg("08006", "connection failure"), KindUnavailable, ""},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), KindTimeout, "deadline"},
		{"canceled", context.Canceled, KindCanceled, ""},
		{"dial", errors.New("dial tcp: connection refused"), KindUnavailable, ""},
	} {
		got := classify(tc.err)
		if got.Kind != tc.kind || got.Reason != tc.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, got.Kind, got.Reason, tc.kind, tc.reason)
		}
		if !errors.Is(got, tc.err) && got.Err != tc.err {
			t.Errorf("%s: cause not preserved", tc.name)
		}
	}
	if k := KindOf(classify(refused("x", "y"))); k != KindRefused {
		t.Errorf("an *Error passes through classify unchanged, got %s", k)
	}
	if KindOf(errors.New("plain")) != "" {
		t.Error("KindOf of a foreign error must be empty")
	}
}

func TestOptionsLimits(t *testing.T) {
	t.Parallel()
	b, err := Options{}.limits()
	want := bounds{rows: DefaultMaxRows, timeout: DefaultTimeout, lockTimeout: DefaultLockTimeout, idleTimeout: DefaultIdleTimeout, maxBytes: MaxResponseBytes}
	if err != nil || b != want {
		t.Fatalf("defaults: %+v %v", b, err)
	}
	max := Options{MaxRows: MaxRowsLimit, Timeout: MaxTimeout, LockTimeout: MaxLockTimeout, IdleTimeout: MaxTimeout, MaxBytes: MaxResponseBytes}
	if b, err = max.limits(); err != nil || b.rows != MaxRowsLimit || b.timeout != MaxTimeout || b.lockTimeout != MaxLockTimeout || b.idleTimeout != MaxTimeout || b.maxBytes != MaxResponseBytes {
		t.Fatalf("maxima must be accepted: %+v %v", b, err)
	}
	for name, o := range map[string]Options{
		"rows over":     {MaxRows: MaxRowsLimit + 1},
		"rows negative": {MaxRows: -1},
		"timeout over":  {Timeout: MaxTimeout + 1},
		"timeout neg":   {Timeout: -1},
		"lock over":     {LockTimeout: MaxLockTimeout + 1},
		"lock neg":      {LockTimeout: -1},
		"idle over":     {IdleTimeout: MaxTimeout + 1},
		"idle neg":      {IdleTimeout: -1},
		"bytes over":    {MaxBytes: MaxResponseBytes + 1},
		"bytes neg":     {MaxBytes: -1},
	} {
		if _, err := o.limits(); err == nil || err.Kind != KindRefused {
			t.Errorf("%s: limits = %v, want refused", name, err)
		}
	}
}

func TestParamRefs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sql string
		want      int
	}{
		{"none", `SELECT 1`, 0},
		{"one", `SELECT $1`, 1},
		{"highest wins, any order", `SELECT $3, $1, $2 + $3`, 3},
		{"gap is the highest", `SELECT $1, $4`, 4},
		{"cast", `SELECT $2::timestamptz`, 2},
		{"multi digit", `SELECT $12`, 12},
		{"in a string", `SELECT '$1', 'it''s $9'`, 0},
		{"in an escape string", `SELECT E'\\'' $7'`, 0},
		{"in a quoted identifier", `SELECT "$5" FROM t`, 0},
		{"in a line comment", "SELECT 1 -- $8\n", 0},
		{"in a block comment", `SELECT /* $8 /* $9 */ $9 */ 1`, 0},
		{"in a dollar quote", `SELECT $$ $1 $$, $tag$ $2 $tag$`, 0},
		{"after a dollar quote", `SELECT $$ $1 $$, $2`, 2},
		{"in an identifier", `SELECT a$1 FROM t`, 0},
		{"real beside fake", `SELECT '$9', $1 -- $8`, 1},
		{"in a subquery", `SELECT * FROM (SELECT $2) x WHERE y = $1`, 2},
	} {
		got, err := ParamRefs(tc.sql)
		if err != nil || got != tc.want {
			t.Errorf("%s: ParamRefs(%q) = %d, %v; want %d", tc.name, tc.sql, got, err, tc.want)
		}
	}
	for name, sql := range map[string]string{
		"zero":         `SELECT $0`,
		"over the max": `SELECT $65536`,
		"huge":         `SELECT $99999999999999999999`,
		"unterminated": `SELECT $1 'oops`,
	} {
		if _, err := ParamRefs(sql); err == nil || KindOf(err) != KindRefused {
			t.Errorf("%s: ParamRefs(%q) err = %v, want refused", name, sql, err)
		}
	}
}

func TestEncodeCell(t *testing.T) {
	t.Parallel()
	const (
		boolOID, int4OID, int8OID, float8OID, numericOID, textOID, jsonbOID = 16, 23, 20, 701, 1700, 25, 3802
	)
	if v, _, _ := encodeCell(int4OID, nil); v != nil {
		t.Errorf("NULL = %v", v)
	}
	if v, _, _ := encodeCell(boolOID, []byte("t")); v != true {
		t.Errorf("bool = %v", v)
	}
	for _, tc := range []struct {
		oid  uint32
		in   string
		want string // JSON encoding
	}{
		{int4OID, "42", `42`},
		{int8OID, "9007199254740993", `"9007199254740993"`},
		{int8OID, "123456789012345", `123456789012345`},
		{numericOID, "3.14159", `3.14159`},
		{numericOID, "NaN", `"NaN"`},
		{float8OID, "Infinity", `"Infinity"`},
		{float8OID, "-1.5e-7", `-1.5e-7`},
		{textOID, `say "hi"`, `"say \"hi\""`},
		{jsonbOID, `{"a": [1, 2]}`, `{"a":[1,2]}`},
		{jsonbOID, `not json`, `"not json"`},
	} {
		v, _, _ := encodeCell(tc.oid, []byte(tc.in))
		if got := mustJSON(t, v); got != tc.want {
			t.Errorf("oid %d %q = %s, want %s", tc.oid, tc.in, got, tc.want)
		}
	}
}

func TestText_CutsOnARuneBoundaryAndSaysSo(t *testing.T) {
	t.Parallel()
	in := strings.Repeat("é", MaxCellBytes) // 2 bytes per rune
	v, size, cut := text([]byte(in))
	s := v.(string)
	if !cut {
		t.Fatal("not cut")
	}
	if !strings.Contains(s, fmt.Sprintf("truncated: %d of %d bytes shown", MaxCellBytes, len(in))) {
		t.Errorf("marker missing: ...%s", s[len(s)-60:])
	}
	if strings.ContainsRune(s, '\uFFFD') {
		t.Error("cut inside a rune")
	}
	if size != len(s) {
		t.Errorf("size %d != len %d", size, len(s))
	}
	if v, _, cut := text([]byte(strings.Repeat("a", MaxCellBytes))); cut || len(v.(string)) != MaxCellBytes {
		t.Error("a cell exactly at the cap must be kept whole")
	}
}

// A call to a function that changes the session, runs SQL text, or leaves
// state behind is refused however it is spelled, and wherever in the statement
// it sits.
func TestCheck_RefusesDeniedFunctions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ sql, reason string }{
		{`SELECT set_config('statement_timeout', '0', false)`, "denied_function"},
		{`SELECT SET_CONFIG('work_mem', '1GB', true)`, "denied_function"},
		{`SELECT "set_config"('work_mem', '1GB', true)`, "denied_function"},
		{`SELECT pg_catalog.set_config('work_mem', '1GB', true)`, "denied_function"},
		{`SELECT "pg_catalog"."set_config"('work_mem', '1GB', true)`, "denied_function"},
		{`SELECT set_config /* gap */ ('work_mem', '1GB', true)`, "denied_function"},
		{"SELECT set_config -- gap\n('work_mem', '1GB', true)", "denied_function"},
		{`WITH x AS (SELECT set_config('work_mem','1GB',true)) SELECT * FROM x`, "denied_function"},
		{`SELECT * FROM (SELECT set_config('lock_timeout','0',false)) s`, "denied_function"},
		{`EXPLAIN SELECT set_config('work_mem','1GB',true)`, "denied_function"},
		// The two review findings: a typed literal is not an E-string, and
		// ts_rewrite runs the text it is given.
		{`SELECT name'\' AS x, set_config('work_mem','1GB',true) AS y, '\' AS z`, "denied_function"},
		{`SELECT x LIKE'\' AS a, set_config('work_mem','1GB',true) AS y, '\' AS z FROM t`, "denied_function"},
		{`SELECT ts_rewrite('a'::tsquery, 'SELECT set_config(''work_mem'',''3GB'',true)::tsquery, ''b''::tsquery')`, "denied_function"},
		// An E-string continued on the next line is read with escapes too.
		{"SELECT E'x'\n'\\' AS a, ' AS b, set_config('work_mem','1GB',true) AS c, 'q' AS d", "denied_function"},
		{"SELECT E'x' -- c\n  '\\' AS a, ' AS b, set_config('work_mem','1GB',true) AS c, 'q' AS d", "denied_function"},
		// A multi-byte identifier continues through $ and before a quote.
		{`SELECT 1 AS é$$, set_config('work_mem','1GB',true) AS y, 2 AS ü$$`, "denied_function"},
		{`SELECT U&"set\005fconfig"('statement_timeout', '0', false)`, "unicode_identifier"},
		// UESCAPE puts a clause between the identifier and "(" (round 3 of the
		// control-plane#1729 review): every U&"..." identifier is refused,
		// wherever it sits.
		{`SELECT U&"set!005fconfig" UESCAPE '!' ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT u&"set!005fconfig" uescape '!' ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT pg_catalog.U&"set!005fconfig" UESCAPE '!' ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT U&"set_config" UESCAPE '!' ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT U&"set!005fconfig" UESCAPE '!' /* gap */ ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT * FROM U&"ts!005fstat" UESCAPE '!' ('SELECT 1')`, "unicode_identifier"},
		{`SELECT U&"query!005fto!005fxml" UESCAPE '!' ('select 1', true, false, '')`, "unicode_identifier"},
		{`SELECT U&"pg!005fadvisory!005flock" UESCAPE '!' (1)`, "unicode_identifier"},
		{`EXPLAIN (ANALYZE) SELECT U&"set!005fconfig" UESCAPE '!' ('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT U&"set_config"('work_mem','1GB',true)`, "unicode_identifier"},
		{`SELECT U&"anything" FROM t`, "unicode_identifier"},
		{`SELECT a FROM U&"t"`, "unicode_identifier"},
		// Attribute notation calls a one-argument function with no parentheses:
		// (x).ts_stat is ts_stat(x). A denied name is refused wherever it is.
		{`SELECT (x)."ts_stat" FROM (SELECT 'SELECT 1'::text AS x) t`, "denied_function"},
		{`SELECT ('SELECT 1'::text).ts_stat`, "denied_function"},
		{`SELECT (1::bigint).pg_advisory_lock`, "denied_function"},
		{`SELECT * FROM ROWS FROM (set_config('work_mem','1GB',true)) x`, "denied_function"},
		{`SELECT set_config FROM t`, "denied_function"},
		{`SELECT "set_config" FROM t`, "denied_function"},
		{`SELECT ts_stat('SELECT 1')`, "denied_function"},
		{`SELECT query_to_xml('select 1', true, false, '')`, "denied_function"},
		{`SELECT query_to_xmlschema('select 1', true, false, '')`, "denied_function"},
		{`SELECT query_to_xml_and_xmlschema('select 1', true, false, '')`, "denied_function"},
		{`SELECT cursor_to_xml('c', 1, true, false, '')`, "denied_function"},
		{`SELECT table_to_xml('t', true, false, '')`, "denied_function"},
		{`SELECT database_to_xml(true, false, '')`, "denied_function"},
		{`SELECT * FROM crosstab('select 1')`, "denied_function"},
		{`SELECT * FROM dblink('dbname=x', 'select 1') AS t(a int)`, "denied_function"},
		{`SELECT dblink_exec('select 1')`, "denied_function"},
		{`SELECT pg_advisory_lock(1)`, "denied_function"},
		{`SELECT pg_try_advisory_xact_lock(1)`, "denied_function"},
		{`SELECT pg_notify('c', 'x')`, "denied_function"},
		{`SELECT pg_terminate_backend(1)`, "denied_function"},
		{`SELECT lo_import('/etc/passwd')`, "denied_function"},
		{`SELECT pg_stat_reset()`, "denied_function"},
		// xpath_table (contrib xml2) builds a query from its arguments and runs
		// it (the forge#1607 review raised work_mem through it).
		{`SELECT * FROM xpath_table('id', 'doc', 'docs', '/a', 'true') AS t(id int, a text)`, "denied_function"},
		{`SELECT * FROM public.XPATH_TABLE('id', 'doc', 'set_config(''work_mem'',''1GB'',true)', '/a', 'true') AS t(id int, a text)`, "denied_function"},
		// The replication functions that change state, by exact name.
		{`SELECT pg_replication_origin_create('o')`, "denied_function"},
		{`SELECT pg_replication_origin_drop('o')`, "denied_function"},
		{`SELECT pg_replication_origin_advance('o', '0/0')`, "denied_function"},
		{`SELECT pg_replication_origin_session_setup('o')`, "denied_function"},
		{`SELECT pg_replication_origin_session_reset()`, "denied_function"},
		{`SELECT pg_replication_origin_xact_setup('0/0', now())`, "denied_function"},
		{`SELECT pg_replication_origin_xact_reset()`, "denied_function"},
		{`SELECT pg_replication_slot_advance('s', '0/0')`, "denied_function"},
		{`SELECT pg_sync_replication_slots()`, "denied_function"},
		{`SELECT pg_create_logical_replication_slot('s', 'pgoutput')`, "denied_function"},
		{`SELECT pg_drop_replication_slot('s')`, "denied_function"},
		{`SELECT * FROM pg_logical_slot_get_changes('s', NULL, NULL)`, "denied_function"},
	} {
		tc := tc
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()
			_, err := Check(tc.sql)
			var e *Error
			if !errors.As(err, &e) || e.Kind != KindRefused || e.Reason != tc.reason {
				t.Fatalf("Check(%q) = %v; want refused/%s", tc.sql, err, tc.reason)
			}
		})
	}
}

// A denied name inside data (a literal, a comment, a dollar-quoted body) is not
// refused, nor is a longer name that merely contains one. A U&'...' STRING is
// data too: only a Unicode-escaped identifier is refused.
func TestCheck_DeniedNamesThatAreNotCallsPass(t *testing.T) {
	t.Parallel()
	for _, sql := range []string{
		`SELECT 'set_config(' AS note`,
		`SELECT $q$set_config('statement_timeout','0',false)$q$ AS note`,
		"SELECT 1 -- set_config('statement_timeout','0',false)\n",
		`SELECT 1 /* set_config(...) */`,
		`SELECT lo_count, ts_rewrite_count FROM t`,
		`SELECT lo_total(x) FROM t`,
		`SELECT name'x' AS typed, E'it\'s' AS escaped`,
		`SELECT 'a' 'b'`,
		"SELECT 'a'\n'b' AS joined",
		`SELECT xpath('/a', '<a/>'::xml)`,
		`SHOW work_mem`,
		`SELECT U&'d\0061t\+000061' AS s, U&'d!0061t' UESCAPE '!' AS t`,
		`SELECT 1 AS reset_count`,
		// The read-only replication views and functions, which replication-lag
		// panels read; only the functions that change state are denied.
		`SELECT slot_name, pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn) AS lag FROM pg_replication_slots`,
		`SELECT * FROM pg_catalog.pg_replication_origin_status`,
		`SELECT * FROM pg_stat_replication`,
		`SELECT * FROM pg_stat_replication_slots`,
		`SELECT pg_replication_origin_oid('o'), pg_replication_origin_progress('o', false)`,
		`SELECT pg_replication_origin_session_is_setup(), pg_replication_origin_session_progress(false)`,
	} {
		if _, err := Check(sql); err != nil {
			t.Errorf("Check(%q) = %v", sql, err)
		}
	}
}

func TestContinuation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		after string
		ok    bool
	}{
		{"\n'", true},
		{"  \n  '", true},
		{" -- c\n'", true},
		{"\n-- c\n'", true},
		{"\r\n'", true},
		{" '", false},      // no newline: a syntax error to Postgres, not a continuation
		{"\nx'", false},    // something else first
		{"/**/\n'", false}, // a block comment ends it
		{"", false},
	} {
		sql := "'a'" + tc.after
		_, got := continuation(sql, 3)
		if got != tc.ok {
			t.Errorf("continuation after %q = %v, want %v", tc.after, got, tc.ok)
		}
	}
}
