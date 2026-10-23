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
		{"SET ROLE postgres", "not_read_only"},
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
	rows, timeout, err := Options{}.limits()
	if err != nil || rows != DefaultMaxRows || timeout != DefaultTimeout {
		t.Fatalf("defaults: %d %s %v", rows, timeout, err)
	}
	if rows, timeout, err = (Options{MaxRows: MaxRowsLimit, Timeout: MaxTimeout}).limits(); err != nil || rows != MaxRowsLimit || timeout != MaxTimeout {
		t.Fatalf("maxima must be accepted: %d %s %v", rows, timeout, err)
	}
	for name, o := range map[string]Options{
		"rows over":     {MaxRows: MaxRowsLimit + 1},
		"rows negative": {MaxRows: -1},
		"timeout over":  {Timeout: MaxTimeout + 1},
		"timeout neg":   {Timeout: -1},
	} {
		if _, _, err := o.limits(); err == nil || err.Kind != KindRefused {
			t.Errorf("%s: limits = %v, want refused", name, err)
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
