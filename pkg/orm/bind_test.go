package orm

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/schema"
)

// bindRow is a model with a column of each kind a value is bound from.
type bindRow struct {
	bun.BaseModel `bun:"table:bind_rows,alias:r"`
	ID            string            `bun:"id,pk"`
	Seq           int64             `bun:"seq,notnull"`
	Body          string            `bun:"body"`
	Note          *string           `bun:"note"`
	Tags          []string          `bun:"tags,array"`
	Meta          map[string]string `bun:"meta,type:jsonb"`
	Blob          []byte            `bun:"blob"`
	Done          bool              `bun:"done,notnull"`
	At            time.Time         `bun:"at,notnull"`
}

// newBindDB is a bun handle over forgeDialect that never connects: these
// tests only render.
func newBindDB(t *testing.T) (*bun.DB, *forgeDialect) {
	t.Helper()
	sqldb, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fd := newForgeDialect()
	return bun.NewDB(sqldb, fd), fd
}

// bindOf renders q as bun does and as a binding render, and returns what
// would be sent.
func bindOf(t *testing.T, db *bun.DB, fd *forgeDialect, q schema.QueryAppender) (inline, stmt string, args []any, ok bool) {
	t.Helper()
	b, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	inline = string(b)
	st := getBindState(fd)
	defer st.release()
	text, err := q.AppendQuery(schema.NewQueryGen(&st.dialect), nil)
	if err != nil {
		t.Fatalf("binding render: %v", err)
	}
	stmt, args, ok = st.bind(text, inline)
	return inline, stmt, args, ok
}

var bindAt = time.Date(2026, 10, 21, 4, 5, 6, 789000000, time.UTC)

func TestBind_ModelColumnsAreBound(t *testing.T) {
	db, fd := newBindDB(t)
	note := "it's"
	row := &bindRow{ID: "01J", Seq: 42, Body: "hello", Note: &note, Tags: []string{"a", "b"},
		Meta: map[string]string{"k": "v"}, Blob: []byte{1, 2}, Done: true, At: bindAt}
	inline, stmt, args, ok := bindOf(t, db, fd, db.NewInsert().Model(row))
	if !ok {
		t.Fatalf("insert not bound: %s", inline)
	}
	want := `INSERT INTO "bind_rows" ("id", "seq", "body", "note", "tags", "meta", "blob", "done", "at") VALUES ($1, ($2::int4), $3, $4, $5, $6, $7, TRUE, $8)`
	if stmt != want {
		t.Fatalf("stmt\n got %s\nwant %s", stmt, want)
	}
	wantArgs := []any{"01J", "42", "hello", "it's", `{"a","b"}`, `{"k":"v"}`, []byte{1, 2}, "2026-10-21 04:05:06.789+00:00"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args\n got %#v\nwant %#v", args, wantArgs)
	}
}

func TestBind_WhereArguments(t *testing.T) {
	db, fd := newBindDB(t)
	for _, tc := range []struct {
		name string
		q    schema.QueryAppender
		stmt string
		args []any
	}{{
		name: "string and time bind, a bare integer stays",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("body = ?", "x").Where("at > ?", bindAt).Where("seq > ?", 7),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (body = $1) AND (at > $2) AND (seq > 7)`,
		args: []any{"x", "2026-10-21 04:05:06.789+00:00"},
	}, {
		name: "Arg binds an integer, typed like its literal",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("seq > ?", Arg(7)).Where("seq < ?", Arg(int64(1)<<40)).Limit(5),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (seq > ($1::int4)) AND (seq < ($2::int8)) LIMIT 5`,
		args: []any{"7", "1099511627776"},
	}, {
		name: "ArgList binds every element",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("seq IN (?)", ArgList([]int{1, 2})).Where("id IN (?)", bun.In([]string{"a", "b"})),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (seq IN (($1::int4), ($2::int4))) AND (id IN ($3, $4))`,
		args: []any{"1", "2", "a", "b"},
	}, {
		name: "a literal written into the SQL stays a literal",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("body = 'fixed' AND seq IN (1, 4)").Where("id = ?", "a"),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (body = 'fixed' AND seq IN (1, 4)) AND (id = $1)`,
		args: []any{"a"},
	}, {
		name: "Literal keeps one value in the text",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("body = ?", Literal("active")).Where("id = ?", "a"),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (body = 'active') AND (id = $1)`,
		args: []any{"a"},
	}, {
		name: "a typed literal, a variadic-any argument and IS NULL stay inline",
		q: db.NewSelect().Model((*bindRow)(nil)).ColumnExpr("jsonb_build_object(?, body)", "k").
			Where("at > now() - INTERVAL ?", "1 day").Where("? IS NULL", "z").Where("lower(body) = lower(?)", "Q"),
		stmt: `SELECT jsonb_build_object('k', body) FROM "bind_rows" AS "r" WHERE (at > now() - INTERVAL '1 day') AND ('z' IS NULL) AND (lower(body) = lower('Q'))`,
	}, {
		name: "positional ORDER BY stays, an ORDER BY expression binds",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").OrderExpr("?", Arg(1)).OrderExpr("body <-> ?", "v"),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" ORDER BY 1, body <-> $1`,
		args: []any{"v"},
	}, {
		name: "an ON CONFLICT predicate stays, the update binds",
		q: db.NewInsert().Model(&bindRow{ID: "x", At: bindAt}).
			On("CONFLICT (id) WHERE body = ? DO UPDATE", "live").Set("body = ?", "new"),
		stmt: `INSERT INTO "bind_rows" AS "r" ("id", "seq", "body", "note", "tags", "meta", "blob", "done", "at") VALUES ($1, ($2::int4), $3, DEFAULT, NULL, $4, NULL, FALSE, $5) ON CONFLICT (id) WHERE body = 'live' DO UPDATE SET body = $6 RETURNING "note"`,
		args: []any{"x", "0", "", "null", "2026-10-21 04:05:06.789+00:00", "new"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			inline, stmt, args, ok := bindOf(t, db, fd, tc.q)
			if tc.args == nil {
				if ok {
					t.Fatalf("bound %s %v; want it sent as rendered", stmt, args)
				}
				if inline != tc.stmt {
					t.Fatalf("inline\n got %s\nwant %s", inline, tc.stmt)
				}
				return
			}
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

func TestBind_RawQuery(t *testing.T) {
	db, fd := newBindDB(t)
	q := db.NewRaw("UPDATE ? SET body = ?, seq = seq + ? WHERE id = ? RETURNING ?", bun.Ident("bind_rows"), "b", Arg(3), "id1", bun.Ident("seq"))
	_, stmt, args, ok := bindOf(t, db, fd, q)
	if !ok {
		t.Fatal("raw statement not bound")
	}
	if want := `UPDATE "bind_rows" SET body = $1, seq = seq + ($2::int4) WHERE id = $3 RETURNING "seq"`; stmt != want {
		t.Fatalf("stmt\n got %s\nwant %s", stmt, want)
	}
	if !reflect.DeepEqual(args, []any{"b", "3", "id1"}) {
		t.Fatalf("args %#v", args)
	}
}

// A statement whose text does not line up with its binding render — here a
// value nested inside an array literal, which lands inside a string — runs
// as rendered.
func TestBind_NestedValueFallsBackToInline(t *testing.T) {
	db, fd := newBindDB(t)
	type elem struct{ A string }
	q := db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("meta @> ?", pgdialect.Array([]elem{{A: "x"}}))
	inline, _, _, ok := bindOf(t, db, fd, q)
	if ok {
		t.Fatalf("bound a statement whose value sits inside a literal: %s", inline)
	}
}

func TestBind_NonDMLIsNotBound(t *testing.T) {
	db, fd := newBindDB(t)
	for _, q := range []schema.QueryAppender{
		db.NewRaw("SET LOCAL statement_timeout = ?", "5s"),
		db.NewRaw("SELECT ?; SELECT ?", "a", "b"),
		db.NewRaw("COMMENT ON TABLE x IS ?", "doc"),
	} {
		if inline, stmt, _, ok := bindOf(t, db, fd, q); ok {
			t.Fatalf("bound %q as %q", inline, stmt)
		}
	}
}

func TestScanParams_SkipsQuotedText(t *testing.T) {
	text := []byte(`SELECT '$1', "$2", $tag$ $3 $tag$, E'\'$4', /* $5 */ $1 -- $6` + "\n, x::int")
	sites, ok := scanParams(text, nil)
	if !ok || len(sites) != 1 || string(text[sites[0].start:sites[0].end]) != "$1" {
		t.Fatalf("sites %+v ok %v", sites, ok)
	}
	if !strings.HasPrefix(string(text[sites[0].end:]), " -- $6") {
		t.Fatal("wrong site")
	}
}

// A slice passed through Arg is a postgres array — the encoding pgx gives a
// []T and a model's `,array` column — on every path a `?` argument reaches:
// a builder's Where, NewRaw, and the WithWhere filters List is built from.
// JSON is never inferred; it is spelled JSON(v). []byte stays bytea.
func TestBind_ArgSliceIsAPostgresArray(t *testing.T) {
	db, fd := newBindDB(t)
	kinds := []string{"a", `b"c`}
	var nilKinds []string
	for _, tc := range []struct {
		name string
		q    schema.QueryAppender
		stmt string
		args []any
	}{{
		name: "builder: a []string is a text array",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("body = ANY(?)", Arg(kinds)),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (body = ANY($1))`,
		args: []any{`{"a","b\"c"}`},
	}, {
		name: "builder: a []int64 is an integer array",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("seq = ANY(?)", Arg([]int64{1, 2})),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (seq = ANY($1))`,
		args: []any{`{1,2}`},
	}, {
		name: "builder: a pointer to a slice is the slice's array",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("tags @> ?", Arg(&kinds)),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (tags @> $1)`,
		args: []any{`{"a","b\"c"}`},
	}, {
		name: "NewRaw: a slice argument is an array",
		q:    db.NewRaw("SELECT id FROM bind_rows WHERE body = ANY(?) AND id = ?", Arg(kinds), "x"),
		stmt: `SELECT id FROM bind_rows WHERE body = ANY($1) AND id = $2`,
		args: []any{`{"a","b\"c"}`, "x"},
	}, {
		name: "List filters: WithWhere Eq on an array column",
		q:    applyOpts(db.NewSelect().Model((*bindRow)(nil)).Column("id"), WhereEq("tags", kinds)),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE ("tags" = $1)`,
		args: []any{`{"a","b\"c"}`},
	}, {
		name: "JSON is spelled, not inferred",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("meta @> ?", Arg(JSON(map[string]string{"k": "v"}))).Where("meta @> ?", JSON(kinds)),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (meta @> $1) AND (meta @> $2)`,
		args: []any{`{"k":"v"}`, `["a","b\"c"]`},
	}, {
		name: "[]byte stays bytea",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("blob = ?", Arg([]byte{1, 2})),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (blob = $1)`,
		args: []any{[]byte{1, 2}},
	}, {
		name: "a nil slice is NULL",
		q:    db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("tags = ?", Arg(nilKinds)),
		stmt: `SELECT "r"."id" FROM "bind_rows" AS "r" WHERE (tags = NULL)`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			inline, stmt, args, ok := bindOf(t, db, fd, tc.q)
			if tc.args == nil {
				if ok {
					t.Fatalf("bound %s %v; want it sent as rendered", stmt, args)
				}
				if inline != tc.stmt {
					t.Fatalf("inline\n got %s\nwant %s", inline, tc.stmt)
				}
				return
			}
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

// An element type pgdialect has no array encoding for does not panic the
// render: it falls back to what bun writes for the value.
func TestBind_ArgUnsupportedSliceDoesNotPanic(t *testing.T) {
	db, _ := newBindDB(t)
	q := db.NewSelect().Model((*bindRow)(nil)).Column("id").Where("meta = ?", Arg([]func(){nil}))
	// bun reports the unsupported value as an error or in the rendered
	// text; either is fine as long as nothing panicked.
	_, _ = q.AppendQuery(db.QueryGen(), nil)
}

func applyOpts(q *bun.SelectQuery, opts ...QueryOption) *bun.SelectQuery {
	for _, o := range opts {
		o(q)
	}
	return q
}
