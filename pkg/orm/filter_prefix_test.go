package orm

import (
	"reflect"
	"testing"
)

func TestEscapeLike(t *testing.T) {
	for in, want := range map[string]string{
		"weekly.":    "weekly.",
		"50%":        `50\%`,
		"a_b":        `a\_b`,
		`C:\dir`:     `C:\\dir`,
		`%_\`:        `\%\_\\`,
		"":           "",
		"métrique.é": "métrique.é",
	} {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWherePrefix_Renders pins the clause a generated List op's prefix
// filter sends: LIKE with an explicit escape character, and the prefix bound
// (escaped, then the one trailing wildcard) as a parameter.
func TestWherePrefix_Renders(t *testing.T) {
	db, fd := newBindDB(t)
	q := db.NewSelect().Model((*bindRow)(nil)).Column("id")
	WherePrefix("body", "50%_off")(q)
	inline, stmt, args, ok := bindOf(t, db, fd, q)
	if !ok {
		t.Fatalf("not bound: %s", inline)
	}
	want := `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE ("body" LIKE $1 ESCAPE '\')`
	if stmt != want {
		t.Fatalf("stmt\n got %s\nwant %s", stmt, want)
	}
	if wantArgs := []any{`50\%\_off%`}; !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}
