package dbbrowse

import (
	"errors"
	"strings"
	"testing"
)

// fixtureDB is a hand-built description: Compile needs nothing else, so the
// compiler's checks run without a server.
func fixtureDB() *Database {
	return &Database{Tables: []Table{
		{
			Schema: "app", Name: "users", Kind: KindTable, EstimatedRows: 7,
			Columns: []Column{
				{Name: "org_id", Type: "uuid", Ordinal: 1, Sortable: true},
				{Name: "id", Type: "integer", Ordinal: 2, Sortable: true},
				{Name: "email", Type: "text", Ordinal: 3, Sortable: true},
				{Name: "doc", Type: "json", Ordinal: 4, Nullable: true},
			},
			PrimaryKey: []string{"org_id", "id"},
		},
		{
			Schema: "app", Name: `we"ird`, Kind: KindView, EstimatedRows: -1,
			Columns: []Column{
				{Name: "b", Type: "text", Ordinal: 1, Sortable: true},
				{Name: "a", Type: "point", Ordinal: 2},
			},
		},
	}}
}

func TestCompileShape(t *testing.T) {
	c, err := Compile(fixtureDB(), Request{
		Table:   "app.users",
		Columns: []string{"email", "id"},
		Filters: []Filter{
			{Column: "email", Op: OpIlike, Values: []string{"%@x.io"}},
			{Column: "id", Op: OpIn, Values: []string{"1", "2"}},
			{Column: "doc", Op: OpEq, Values: []string{`{"a":1}`}},
			{Column: "doc", Op: OpNotNull},
		},
		Sort:   []Sort{{Column: "email", Desc: true}},
		Limit:  10,
		Offset: 20,
		Count:  CountExact,
	})
	if err != nil {
		t.Fatal(err)
	}
	const where = ` WHERE "email"::text ILIKE $1 AND "id" IN ($2, $3) AND "doc"::text = $4 AND "doc" IS NOT NULL`
	page := c.Page()
	want := `SELECT "email", "id" FROM "app"."users"` + where + ` ORDER BY "email" DESC, "org_id", "id" LIMIT 11 OFFSET 20`
	if page.SQL != want {
		t.Errorf("page SQL\n got: %s\nwant: %s", page.SQL, want)
	}
	if got := len(page.Args); got != 4 {
		t.Errorf("page args = %d, want 4", got)
	}
	cq, ok := c.CountQuery()
	if !ok {
		t.Fatal("exact count has no count statement")
	}
	if wantC := `SELECT count(*) FROM (SELECT 1 FROM "app"."users"` + where + ` LIMIT 10001) AS capped`; cq.SQL != wantC {
		t.Errorf("count SQL\n got: %s\nwant: %s", cq.SQL, wantC)
	}
}

// A table without a primary key orders by every sortable column, so a page
// boundary is still deterministic; a name with a quote is quoted, not
// spliced.
func TestCompileNoPrimaryKey(t *testing.T) {
	c, err := Compile(fixtureDB(), Request{Table: `app.we"ird`})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "b", "a" FROM "app"."we""ird" ORDER BY "b" LIMIT 51 OFFSET 0`
	if got := c.Page().SQL; got != want {
		t.Errorf("SQL\n got: %s\nwant: %s", got, want)
	}
}

// A key naming a column the role cannot read never reaches ORDER BY: the
// readable sortable columns order the page instead.
func TestCompileKeyWithHiddenColumn(t *testing.T) {
	db := &Database{Tables: []Table{{
		Schema: "app", Name: "memberships", Kind: KindTable,
		Columns: []Column{
			{Name: "user_id", Type: "integer", Ordinal: 2, Sortable: true},
			{Name: "role", Type: "text", Ordinal: 3, Sortable: true},
		},
		PrimaryKey: []string{"org_id", "user_id"},
	}}}
	c, err := Compile(db, Request{Table: "app.memberships"})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "user_id", "role" FROM "app"."memberships" ORDER BY "user_id", "role" LIMIT 51 OFFSET 0`
	if got := c.Page().SQL; got != want {
		t.Errorf("SQL\n got: %s\nwant: %s", got, want)
	}
}

func TestCompileEstimatedCount(t *testing.T) {
	c, err := Compile(fixtureDB(), Request{Table: "app.users", Count: CountEstimated})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.CountQuery(); ok || c.estimate != 7 {
		t.Errorf("unfiltered estimate: count statement %v, estimate %d; want none and 7", ok, c.estimate)
	}
	c, err = Compile(fixtureDB(), Request{Table: "app.users", Count: CountEstimated,
		Filters: []Filter{{Column: "id", Op: OpGt, Values: []string{"3"}}}})
	if err != nil {
		t.Fatal(err)
	}
	q, ok := c.CountQuery()
	if !ok || !strings.HasPrefix(q.SQL, "EXPLAIN (FORMAT JSON) SELECT 1 FROM ") {
		t.Errorf("filtered estimate = %q, want an EXPLAIN", q.SQL)
	}
}

// Every value is a parameter: whatever it holds, it is absent from the SQL.
func TestCompileBindsValues(t *testing.T) {
	evil := []string{`x' OR '1'='1`, `'; DROP TABLE app.users; --`, `$1 OR $2`, `") OR true --`}
	for _, v := range evil {
		c, err := Compile(fixtureDB(), Request{Table: "app.users", Filters: []Filter{
			{Column: "email", Op: OpEq, Values: []string{v}},
			{Column: "email", Op: OpIn, Values: []string{v, v}},
		}})
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		page := c.Page()
		if strings.Contains(page.SQL, v) || strings.Contains(page.SQL, "'") {
			t.Errorf("value %q reached the SQL: %s", v, page.SQL)
		}
		for i, a := range page.Args {
			if a != v {
				t.Errorf("%q: arg %d = %v", v, i, a)
			}
		}
	}
}

func TestCompileRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
		want error
	}{
		{"unknown table", Request{Table: "app.nope"}, ErrUnknownTable},
		{"unqualified table", Request{Table: "users"}, ErrUnknownTable},
		{"table injection", Request{Table: `app.users; DROP TABLE app.users`}, ErrUnknownTable},
		{"quoted table spelling", Request{Table: `"app"."users"`}, ErrUnknownTable},
		{"unknown column", Request{Table: "app.users", Columns: []string{"password_hash"}}, ErrUnknownColumn},
		{"column injection", Request{Table: "app.users", Columns: []string{`id", "password_hash`}}, ErrUnknownColumn},
		{"column case", Request{Table: "app.users", Columns: []string{"ID"}}, ErrUnknownColumn},
		{"filter column", Request{Table: "app.users", Filters: []Filter{{Column: "id) OR (1=1", Op: OpEq, Values: []string{"1"}}}}, ErrUnknownColumn},
		{"sort column", Request{Table: "app.users", Sort: []Sort{{Column: "1; SELECT pg_sleep(10)"}}}, ErrUnknownColumn},
		{"unknown op", Request{Table: "app.users", Filters: []Filter{{Column: "id", Op: "= 1 OR 1 =", Values: []string{"1"}}}}, ErrUnknownOp},
		{"empty op", Request{Table: "app.users", Filters: []Filter{{Column: "id", Values: []string{"1"}}}}, ErrUnknownOp},
		{"eq without value", Request{Table: "app.users", Filters: []Filter{{Column: "id", Op: OpEq}}}, ErrInvalidRequest},
		{"is_null with value", Request{Table: "app.users", Filters: []Filter{{Column: "id", Op: OpIsNull, Values: []string{"x"}}}}, ErrInvalidRequest},
		{"empty in", Request{Table: "app.users", Filters: []Filter{{Column: "id", Op: OpIn}}}, ErrInvalidRequest},
		{"huge in", Request{Table: "app.users", Filters: []Filter{{Column: "id", Op: OpIn, Values: make([]string, MaxInValues+1)}}}, ErrInvalidRequest},
		{"NUL value", Request{Table: "app.users", Filters: []Filter{{Column: "email", Op: OpEq, Values: []string{"a\x00b"}}}}, ErrInvalidRequest},
		{"bad UTF-8", Request{Table: "app.users", Filters: []Filter{{Column: "email", Op: OpEq, Values: []string{"\xff"}}}}, ErrInvalidRequest},
		{"long value", Request{Table: "app.users", Filters: []Filter{{Column: "email", Op: OpEq, Values: []string{strings.Repeat("a", MaxValueBytes+1)}}}}, ErrInvalidRequest},
		{"order on json", Request{Table: "app.users", Filters: []Filter{{Column: "doc", Op: OpLt, Values: []string{"{}"}}}}, ErrInvalidRequest},
		{"sort on json", Request{Table: "app.users", Sort: []Sort{{Column: "doc"}}}, ErrInvalidRequest},
		{"sort twice", Request{Table: "app.users", Sort: []Sort{{Column: "id"}, {Column: "id", Desc: true}}}, ErrInvalidRequest},
		{"select twice", Request{Table: "app.users", Columns: []string{"id", "id"}}, ErrInvalidRequest},
		{"limit too big", Request{Table: "app.users", Limit: MaxLimit + 1}, ErrInvalidRequest},
		{"negative limit", Request{Table: "app.users", Limit: -1}, ErrInvalidRequest},
		{"negative offset", Request{Table: "app.users", Offset: -1}, ErrInvalidRequest},
		{"bad count", Request{Table: "app.users", Count: "all"}, ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(fixtureDB(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v does not wrap ErrInvalidRequest", err)
			}
		})
	}
}

func TestDatabaseTableAmbiguous(t *testing.T) {
	d := &Database{Tables: []Table{{Schema: "a.b", Name: "c"}, {Schema: "a", Name: "b.c"}}}
	if _, err := d.Table("a.b.c"); !errors.Is(err, ErrUnknownTable) {
		t.Fatalf("err = %v, want ErrUnknownTable", err)
	}
}
