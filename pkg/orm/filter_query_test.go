package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/uptrace/bun/schema"
)

// TestQueryOptions_Render pins the SQL, and the bound arguments, each filter /
// ordering / paging option produces. Values travel as parameters; identifiers
// are quoted.
func TestQueryOptions_Render(t *testing.T) {
	db, fd := newBindDB(t)
	sel := func(opts ...QueryOption) schema.QueryAppender {
		return applyOpts(db.NewSelect().Model((*bindRow)(nil)).Column("id"), opts...)
	}
	const head = `SELECT "r"."id" FROM "bind_rows" AS "r"`

	for _, tc := range []struct {
		name string
		opts []QueryOption
		stmt string
		args []any
	}{
		{"WhereEq", []QueryOption{WhereEq("body", "x")}, head + ` WHERE ("body" = $1)`, []any{"x"}},
		{"WhereNotEq", []QueryOption{WhereNotEq("body", "x")}, head + ` WHERE ("body" != $1)`, []any{"x"}},
		{"WhereGt", []QueryOption{WhereGt("seq", 3)}, head + ` WHERE ("seq" > ($1::int4))`, []any{"3"}},
		{"WhereGte", []QueryOption{WhereGte("seq", 3)}, head + ` WHERE ("seq" >= ($1::int4))`, []any{"3"}},
		{"WhereLt", []QueryOption{WhereLt("seq", 3)}, head + ` WHERE ("seq" < ($1::int4))`, []any{"3"}},
		{"WhereLte", []QueryOption{WhereLte("seq", 3)}, head + ` WHERE ("seq" <= ($1::int4))`, []any{"3"}},
		{"WhereLike keeps wildcards live", []QueryOption{WhereLike("body", "a%")}, head + ` WHERE ("body" LIKE $1)`, []any{"a%"}},
		{"WhereILike", []QueryOption{WhereILike("body", "A%")}, head + ` WHERE ("body" ILIKE $1)`, []any{"A%"}},
		{"WhereIn binds each element", []QueryOption{WhereIn("id", []string{"a", "b"})}, head + ` WHERE ("id" IN ($1, $2))`, []any{"a", "b"}},
		{"WhereNotIn binds each element", []QueryOption{WhereNotIn("id", []string{"a", "b"})}, head + ` WHERE ("id" NOT IN ($1, $2))`, []any{"a", "b"}},
		{"WhereIsNull", []QueryOption{WhereIsNull("note")}, head + ` WHERE ("note" IS NULL)`, nil},
		{"WhereIsNotNull", []QueryOption{WhereIsNotNull("note")}, head + ` WHERE ("note" IS NOT NULL)`, nil},
		{"WithWhere with an unknown comparison spells the operator", []QueryOption{WithWhere("seq", Operator("<>"), "x")}, head + ` WHERE ("seq" <> $1)`, []any{"x"}},
		{"WhereILikeAny ORs the columns inside one group",
			[]QueryOption{WhereILikeAny([]string{"body", "note"}, "%q%"), WhereEq("id", "a")},
			head + ` WHERE (("body" ILIKE $1) OR ("note" ILIKE $2)) AND ("id" = $3)`, []any{"%q%", "%q%", "a"}},
		{"WhereILikeAny with no columns adds nothing", []QueryOption{WhereILikeAny(nil, "x")}, head, nil},
		{"WithOrderBy applies the direction to every bare column",
			[]QueryOption{WithOrderBy("seq, body", Desc)}, head + ` ORDER BY "seq" DESC, "body" DESC`, nil},
		{"WithOrderBy honors a column's own direction and skips blanks",
			[]QueryOption{WithOrderBy("seq asc, , body", Desc)}, head + ` ORDER BY "seq" ASC, "body" DESC`, nil},
		{"WithLimit and WithOffset", []QueryOption{WithLimit(5), WithOffset(10)}, head + ` LIMIT 5 OFFSET 10`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inline, stmt, args, ok := bindOf(t, db, fd, sel(tc.opts...))
			if tc.args == nil {
				// Nothing to bind: the statement goes out as rendered.
				if ok {
					t.Fatalf("bound %s %v, want the statement sent as rendered", stmt, args)
				}
				if inline != tc.stmt {
					t.Fatalf("stmt\n got %s\nwant %s", inline, tc.stmt)
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

// An empty IN list renders NULL, so it matches no row rather than being a
// syntax error.
func TestWhereIn_EmptyListMatchesNothing(t *testing.T) {
	db, _ := newBindDB(t)
	q := applyOpts(db.NewSelect().Model((*bindRow)(nil)).Column("id"), WhereIn("id", []string{}))
	if got := q.String(); !strings.HasSuffix(got, `WHERE ("id" IN (NULL))`) {
		t.Fatalf("query = %s", got)
	}
}

func TestValidateOrderBy(t *testing.T) {
	allowed := []string{"id", "Created_At"}
	for _, tc := range []struct {
		name    string
		clause  string
		allowed []string
		wantErr string // substring; "" means valid
	}{
		{"empty clause is no ordering", "", allowed, ""},
		{"a declared column", "id", allowed, ""},
		{"allowlist is case-insensitive", "CREATED_AT desc", allowed, ""},
		{"several columns with directions", "id ASC, created_at Desc", allowed, ""},
		{"no allowlist accepts any identifier", "anything_at_all", nil, ""},
		{"an undeclared column", "password_hash", allowed, `unknown order-by column "password_hash" (allowed: id, Created_At)`},
		{"an empty element", "id,,created_at", allowed, "empty order-by clause"},
		{"a trailing comma", "id,", allowed, "empty order-by clause"},
		{"three tokens", "id DESC NULLS", allowed, `invalid order-by clause: "id DESC NULLS"`},
		{"an injected expression", "id; DROP TABLE users", allowed, "invalid order-by clause"},
		{"a non-identifier character", "id)", nil, `invalid character ")" in order-by column "id)"`},
		{"a bad direction", "id SIDEWAYS", allowed, `invalid order-by direction "SIDEWAYS" (must be ASC or DESC)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOrderBy(tc.clause, tc.allowed)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateOrderBy(%q) = %v, want nil", tc.clause, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateOrderBy(%q) = %v, want error containing %q", tc.clause, err, tc.wantErr)
			}
		})
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	for _, id := range []string{"01J0000000000000000000000A", "a/b+c==", "ünïcode id", "x"} {
		tok := EncodeCursor(id)
		if strings.ContainsAny(tok, "+/=") {
			t.Errorf("EncodeCursor(%q) = %q: not URL-safe unpadded base64", id, tok)
		}
		got, err := DecodeCursor(tok)
		if err != nil || got != id {
			t.Errorf("DecodeCursor(EncodeCursor(%q)) = %q, %v", id, got, err)
		}
	}
}

func TestDecodeCursor_Errors(t *testing.T) {
	if _, err := DecodeCursor(""); err == nil || err.Error() != "empty cursor token" {
		t.Errorf("empty token: err = %v", err)
	}
	_, err := DecodeCursor("not base64!")
	if err == nil || !strings.HasPrefix(err.Error(), "invalid cursor token: ") {
		t.Errorf("garbage token: err = %v, want an invalid cursor token error", err)
	}
	// Padded base64 is not what EncodeCursor emits, so it is refused.
	if _, err := DecodeCursor("YQ=="); err == nil {
		t.Error("a padded token decoded; cursors are unpadded base64url")
	}
}
