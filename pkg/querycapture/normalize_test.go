package querycapture

import (
	"reflect"
	"testing"
)

func TestNormalize_SQL(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"whitespace and comments", "SELECT  a,\n\tb -- trailing\nFROM /* x /* nested */ y */ t\n WHERE a = $1;", "SELECT a, b FROM t WHERE a = $1"},
		{"IN list of placeholders collapses and renumbers",
			`SELECT * FROM "t" WHERE (id = $1) AND (body IN ($2, $3, $4)) AND (n = $5)`,
			`SELECT * FROM "t" WHERE (id = $1) AND (body IN ($2)) AND (n = $3)`},
		{"IN list of literals collapses", `SELECT * FROM t WHERE id IN (1, 2, 3)`, `SELECT * FROM t WHERE id IN (1)`},
		{"IN list of casts collapses", `SELECT * FROM t WHERE id IN ($1::int8, $2::int8)`, `SELECT * FROM t WHERE id IN ($1::int8)`},
		{"IN subquery is kept", `SELECT * FROM t WHERE id IN (SELECT id FROM u WHERE x = $1)`, `SELECT * FROM t WHERE id IN (SELECT id FROM u WHERE x = $1)`},
		{"IN of columns is kept", `SELECT * FROM t WHERE $1 IN (a, b)`, `SELECT * FROM t WHERE $1 IN (a, b)`},
		{"VALUES rows collapse",
			`INSERT INTO "t" ("id", "body") VALUES ($1, $2), ($3, DEFAULT), ($4, $5) RETURNING "id"`,
			`INSERT INTO "t" ("id", "body") VALUES ($1, $2) RETURNING "id"`},
		{"repeated placeholder keeps one number", `SELECT * FROM t WHERE a = $2 OR b = $2 OR c = $1`, `SELECT * FROM t WHERE a = $1 OR b = $1 OR c = $2`},
		{"literals stay", `SELECT * FROM t WHERE status = 'open' AND deleted_at IS NULL LIMIT 10`, `SELECT * FROM t WHERE status = 'open' AND deleted_at IS NULL LIMIT 10`},
		{"quotes protect what looks like syntax",
			`SELECT 'a -- not a comment', "col IN (1,2)", E'it\'s', $q$ IN ($1, $2) $q$ FROM t`,
			`SELECT 'a -- not a comment', "col IN (1,2)", E'it\'s', $q$ IN ($1, $2) $q$ FROM t`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in).SQL; got != tc.want {
				t.Errorf("Normalize(%q).SQL\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalize_Fingerprint(t *testing.T) {
	same := [][2]string{
		{`SELECT * FROM t WHERE id = 'a' LIMIT 10`, `select *  from T where ID = 'bbb' limit 50`},
		{`SELECT * FROM t WHERE id IN ($1, $2)`, `SELECT * FROM t WHERE id IN ($1, $2, $3, $4)`},
		{`INSERT INTO t (a) VALUES ($1)`, `INSERT INTO t (a) VALUES ($1), ($2)`},
		{`SELECT a FROM t WHERE a=$1`, "SELECT a\nFROM t -- c\nWHERE a = $1;"},
		{`SELECT x FROM t WHERE n > 1.5e3`, `SELECT x FROM t WHERE n > 7`},
	}
	for _, p := range same {
		if a, b := Normalize(p[0]).Fingerprint, Normalize(p[1]).Fingerprint; a != b {
			t.Errorf("want one fingerprint for\n  %q (%s)\n  %q (%s)", p[0], a, p[1], b)
		}
	}
	different := [][2]string{
		{`SELECT * FROM t WHERE a = $1`, `SELECT * FROM t WHERE b = $1`},
		{`SELECT * FROM "T"`, `SELECT * FROM "t"`},                        // quoted identifiers are case-sensitive
		{`SELECT * FROM t1`, `SELECT * FROM t2`},                          // a digit in a name is not a literal
		{`SELECT * FROM t WHERE a = $1`, `SELECT * FROM t WHERE a = 'x'`}, // a parameter is not a literal
		{`SELECT * FROM t WHERE id IN (SELECT 1)`, `SELECT * FROM t WHERE id IN (SELECT 2, 3)`},
	}
	for _, p := range different {
		if a, b := Normalize(p[0]).Fingerprint, Normalize(p[1]).Fingerprint; a == b {
			t.Errorf("want different fingerprints for %q and %q, both %s", p[0], p[1], a)
		}
	}
	if fp := Normalize("SELECT 1").Fingerprint; len(fp) != 16 {
		t.Errorf("fingerprint %q: want 16 hex digits", fp)
	}
}

func TestNormalize_OperationAndTables(t *testing.T) {
	for _, tc := range []struct {
		in     string
		op     string
		tables []string
	}{
		{`SELECT "n"."id" FROM "notes" AS "n" JOIN public.users u ON u.id = n.user_id WHERE n.id = $1`, "SELECT", []string{"notes", "public.users"}},
		{`INSERT INTO "notes" ("id") VALUES ($1) ON CONFLICT ("id") DO UPDATE SET id = EXCLUDED.id`, "INSERT", []string{"notes"}},
		{`UPDATE ONLY notes SET body = $1 WHERE id = $2`, "UPDATE", []string{"notes"}},
		{`delete from "Notes" where id = $1`, "DELETE", []string{"Notes"}},
		{`WITH recent AS (SELECT * FROM notes WHERE ts > $1) SELECT * FROM recent JOIN users ON true`, "SELECT", []string{"notes", "users"}},
		{`SELECT EXTRACT(EPOCH FROM ts), a FROM (SELECT * FROM t) s WHERE x IS DISTINCT FROM $1`, "SELECT", []string{"t"}},
		{`SET TRANSACTION READ ONLY DEFERRABLE`, "SET", nil},
	} {
		st := Normalize(tc.in)
		if st.Operation != tc.op || !reflect.DeepEqual(st.Tables, tc.tables) {
			t.Errorf("Normalize(%q): operation %q tables %q; want %q %q", tc.in, st.Operation, st.Tables, tc.op, tc.tables)
		}
	}
}

func TestExplainable(t *testing.T) {
	for _, q := range []string{"SELECT 1", "WITH x AS (SELECT 1) DELETE FROM t", "values (1)", "INSERT INTO t VALUES ($1)"} {
		if !Explainable(Normalize(q).Operation) {
			t.Errorf("%q: want explainable", q)
		}
	}
	for _, q := range []string{"BEGIN", "CREATE TABLE t (a int)", "SET LOCAL x = 1", "WITH x AS (SELECT 1)", ""} {
		if Explainable(Normalize(q).Operation) {
			t.Errorf("%q: want not explainable", q)
		}
	}
}
