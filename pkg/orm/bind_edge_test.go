package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/schema"
)

func TestIsNumeric(t *testing.T) {
	for in, want := range map[string]bool{
		"0": true, "42": true, "-7": true, "1.5": true, "-0.25": true,
		"1e5": true, "1E+5": true, "2.5e-3": true,
		"": false, "-": false, "1.": false, ".5": false, "1e": false, "1e+": false,
		"1.5.2": false, "12abc": false, "--1": false, "0x10": false, "+1": false,
	} {
		if got := isNumeric([]byte(in)); got != want {
			t.Errorf("isNumeric(%q) = %v, want %v", in, got, want)
		}
	}
}

// numericCast is the type postgres gives the literal, so a bound number keeps
// the overload resolution and index-vs-key meaning the literal had.
func TestNumericCast(t *testing.T) {
	for in, want := range map[string]string{
		"0":                    "int4",
		"2147483647":           "int4",
		"-2147483648":          "int4",
		"2147483648":           "int8",
		"-2147483649":          "int8",
		"9223372036854775807":  "int8",
		"9223372036854775808":  "numeric", // overflows int8
		"1.5":                  "numeric",
		"1e3":                  "numeric",
		"-3E2":                 "numeric",
		"99999999999999999999": "numeric",
	} {
		if got := numericCast([]byte(in)); got != want {
			t.Errorf("numericCast(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestUnquoteLiteral(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{`'plain'`, "plain", true},
		{`''`, "", true},
		{`'it''s'`, "it's", true},
		{`'a''''b'`, "a''b", true},
		{`'lone ' quote'`, "", false}, // an undoubled inner quote is not one literal
		{`'ends in one'''`, "ends in one'", true},
		{`'dangling''`, "", false},
		{`not quoted`, "", false},
		{`'`, "", false},
	} {
		got, ok := unquoteLiteral([]byte(tc.in))
		if got != tc.want || ok != tc.ok {
			t.Errorf("unquoteLiteral(%s) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsByteaLiteral(t *testing.T) {
	for in, want := range map[string]bool{
		`'\x0102'`: true, `'\x'`: true,
		`'\x0102`: false, `'x0102'`: false, `\x0102`: false, `''`: false,
	} {
		if got := isByteaLiteral([]byte(in)); got != want {
			t.Errorf("isByteaLiteral(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestParamIndex(t *testing.T) {
	if got := paramIndex([]byte("$12")); got != 12 {
		t.Errorf("paramIndex($12) = %d", got)
	}
	if got := paramIndex([]byte("$x")); got != -1 {
		t.Errorf("paramIndex($x) = %d, want -1 for a non-number", got)
	}
}

// scanParams refuses text it cannot prove it understood: unterminated quoting,
// a second statement, or a statement kind that takes no parameters.
func TestScanParams_RefusesWhatItCannotProve(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		ok   bool
		n    int // parameter count when ok
	}{
		{"a select with two params", `SELECT a FROM t WHERE x = $1 AND y = $2`, true, 2},
		{"a leading comment and CTE", "/* c */ WITH x AS (SELECT $1) SELECT * FROM x", true, 1},
		{"a nested block comment hides a param", `SELECT /* a /* $9 */ $9 */ $1`, true, 1},
		{"an unterminated block comment", `SELECT $1 /* oops`, false, 0},
		{"an unterminated string", `SELECT 'abc, $1`, false, 0},
		{"an unterminated quoted identifier", `SELECT "abc, $1`, false, 0},
		{"an unterminated dollar quote", `SELECT $tag$ abc $1`, false, 0},
		{"a dollar quote hides a param", `SELECT $q$ $1 $q$, $1`, true, 1},
		{"an escape string honours a backslash quote", `SELECT E'it\'s $1', $1`, true, 1},
		{"a unicode-escape string", `SELECT U&'d\0061t' , $1`, true, 1},
		{"a unicode-escape identifier", `SELECT U&"d\0061t" FROM t WHERE x = $1`, true, 1},
		{"a bit string prefix", `SELECT B'101', $1`, true, 1},
		{"a second statement", `SELECT $1; SELECT $2`, false, 0},
		{"a trailing semicolon is one statement", `SELECT $1;`, true, 1},
		{"DDL takes no parameters", `CREATE TABLE t (a int DEFAULT $1)`, false, 0},
		{"empty text", ``, false, 0},
		{"only a comment", `-- nothing`, false, 0},
		{"a parenthesized statement", `(SELECT $1) UNION (SELECT $2)`, true, 2},
		{"a cast and an operator run", `SELECT x::int, a+--c` + "\n" + `$1`, true, 1},
		{"an exponent number", `SELECT 1e-5, $1`, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sites, ok := scanParams([]byte(tc.sql), nil)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (sites %+v)", ok, tc.ok, sites)
			}
			if ok && len(sites) != tc.n {
				t.Fatalf("found %d params, want %d", len(sites), tc.n)
			}
		})
	}
}

// bindOf renders a statement and reports what is sent: it must refuse to bind
// whenever the text it would send could differ from the statement bun rendered.
func TestBind_ValueClasses(t *testing.T) {
	db, fd := newBindDB(t)
	sel := func() *bun.SelectQuery {
		return db.NewSelect().Model((*bindRow)(nil)).Column("id")
	}
	for _, tc := range []struct {
		name string
		q    schema.QueryAppender
		stmt string
		args []any
	}{
		{
			name: "[]byte binds as bytea, bytes intact",
			q:    sel().Where("blob = ?", []byte{0xde, 0xad}),
			stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (blob = $1)`,
			args: []any{[]byte{0xde, 0xad}},
		},
		{
			name: "a negative number after a minus keeps its separating space",
			q:    db.NewSelect().Model((*bindRow)(nil)).ColumnExpr("seq - ?", Arg(-1)).Where("id = ?", "a"),
			stmt: `SELECT seq - ($1::int4) FROM "bind_rows" AS "r" WHERE (id = $2)`,
			args: []any{"-1", "a"},
		},
		{
			name: "numbers are typed as postgres types their literals",
			q:    sel().Where("seq > ?", Arg(-5)).Where("seq < ?", Arg(1.5)).Where("seq < ?", Arg(int64(1)<<40)),
			stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (seq > ($1::int4)) AND (seq < ($2::numeric)) AND (seq < ($3::int8))`,
			args: []any{"-5", "1.5", "1099511627776"},
		},
		{
			name: "a quote in a string value is data, not syntax",
			q:    sel().Where("body = ?", "a'b; DROP TABLE x"),
			stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (body = $1)`,
			args: []any{"a'b; DROP TABLE x"},
		},
		{
			name: "a nested ArgList is a tuple list, mixed element types each bound",
			q: sel().Where("x IN (?)", ArgList([][]int{{1, 2}, {3}})).
				Where("y IN (?)", ArgList([]any{"a", 2, []byte{9}})).
				Where("z IN (?)", ArgList([]string{})),
			stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (x IN ((($1::int4), ($2::int4)), (($3::int4)))) AND (y IN ($4, ($5::int4), $6)) AND (z IN (NULL))`,
			args: []any{"1", "2", "3", "a", "2", []byte{9}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inline, stmt, args, ok := bindOf(t, db, fd, tc.q)
			if !ok {
				t.Fatalf("not bound: %s", inline)
			}
			if stmt != tc.stmt {
				t.Fatalf("stmt\n got %s\nwant %s", stmt, tc.stmt)
			}
			if !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("args\n got %#v\nwant %#v", args, tc.args)
			}
		})
	}
}

// ArgList over something that is not a slice is bun.In's: the error surfaces
// in the rendered text and nothing is bound.
func TestBind_ArgListOfANonSliceIsNotBound(t *testing.T) {
	db, fd := newBindDB(t)
	q := db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("x IN (?)", ArgList("not a slice"))
	inline, _, _, ok := bindOf(t, db, fd, q)
	if ok {
		t.Fatalf("bound a statement bun could not render: %s", inline)
	}
	if !strings.Contains(inline, "non-slice") {
		t.Fatalf("inline = %s, want bun's non-slice marker", inline)
	}
}

// A statement over maxParams values stays inline rather than overflowing
// postgres's parameter limit.
func TestBind_TooManyValuesStaysInline(t *testing.T) {
	db, fd := newBindDB(t)
	ids := make([]string, maxParams+5)
	for i := range ids {
		ids[i] = "i"
	}
	q := db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("id IN (?)", ArgList(ids))
	inline, _, _, ok := bindOf(t, db, fd, q)
	if ok {
		t.Fatal("bound more than maxParams values")
	}
	if !strings.Contains(inline, "'i', 'i'") {
		t.Fatalf("the values were not left inline: %.80s…", inline)
	}
}

// bindState.bind rejects a render whose text and bun's own statement disagree.
func TestBindState_BindRefusesAMismatchedRender(t *testing.T) {
	_, fd := newBindDB(t)
	st := getBindState(fd)
	defer st.release()
	st.dialect.AppendString(nil, "v") // records one value, literal 'v'
	if len(st.values) != 1 {
		t.Fatalf("recorded %d values, want 1", len(st.values))
	}
	for name, tc := range map[string]struct{ text, inline string }{
		"text is not the statement bun rendered": {`SELECT $1`, `SELECT 'w'`},
		"parameter count differs":                {`SELECT $1, $2`, `SELECT 'v', 'v'`},
		"parameters out of order":                {`SELECT $2`, `SELECT 'v'`},
		"trailing text differs":                  {`SELECT $1 FROM a`, `SELECT 'v' FROM b`},
	} {
		if q, args, ok := st.bind([]byte(tc.text), tc.inline); ok {
			t.Errorf("%s: bound %q %v", name, q, args)
		}
	}
	if q, args, ok := st.bind([]byte(`SELECT $1`), `SELECT 'v'`); !ok || q != `SELECT $1` || !reflect.DeepEqual(args, []any{"v"}) {
		t.Errorf("a consistent render: %q %v %v", q, args, ok)
	}
}
