package dbbrowse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbquery"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// fixture is a database holding a schema with single, composite and
// self-referencing foreign keys, plus a reader role granted part of it, and a
// connection as each.
type fixture struct {
	admin, reader *pgx.Conn
	db            *Database
}

const fixtureSQL = `
CREATE SCHEMA app;
CREATE TYPE app.mood AS ENUM ('sad', 'ok', 'happy');
CREATE TABLE app.orgs (
	id         uuid PRIMARY KEY,
	name       text NOT NULL,
	created_at timestamptz NOT NULL
);
COMMENT ON TABLE app.orgs IS 'tenants';
CREATE TABLE app.users (
	org_id        uuid NOT NULL REFERENCES app.orgs (id),
	id            int NOT NULL,
	email         text NOT NULL,
	password_hash text,
	manager_id    int,
	mood          app.mood,
	tags          text[],
	meta          jsonb,
	doc           json,
	PRIMARY KEY (org_id, id),
	CONSTRAINT users_manager_fkey FOREIGN KEY (org_id, manager_id) REFERENCES app.users (org_id, id)
);
COMMENT ON COLUMN app.users.password_hash IS 'forge:secret';
COMMENT ON COLUMN app.users.email IS 'login';
CREATE TABLE app.posts (
	id        int PRIMARY KEY,
	org_id    uuid NOT NULL,
	author_id int NOT NULL,
	title     text,
	CONSTRAINT posts_author_fkey FOREIGN KEY (org_id, author_id) REFERENCES app.users (org_id, id) ON DELETE CASCADE
);
-- Not granted to the reader: neither it nor its reference to users may show.
CREATE TABLE app.audit (
	id       int PRIMARY KEY,
	user_org uuid,
	user_id  int,
	FOREIGN KEY (user_org, user_id) REFERENCES app.users (org_id, id)
);
-- References users through a column the reader may not read.
CREATE TABLE app.sessions (
	id      int PRIMARY KEY,
	org_id  uuid,
	user_id int,
	note    text,
	FOREIGN KEY (org_id, user_id) REFERENCES app.users (org_id, id)
);
-- Its key has a column the reader may not read.
CREATE TABLE app.memberships (
	org_id  uuid NOT NULL,
	user_id int NOT NULL,
	role    text NOT NULL,
	PRIMARY KEY (org_id, user_id)
);
CREATE TABLE app.events (id int PRIMARY KEY);
CREATE TABLE app.notes (id int PRIMARY KEY, body text, title text);
CREATE VIEW app.post_titles AS SELECT id, title FROM app.posts;
CREATE TABLE app."we""ird; DROP TABLE app.orgs; --" (id int PRIMARY KEY, "col; --" text);
CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);

INSERT INTO app.orgs VALUES
	('00000000-0000-0000-0000-00000000000a', 'acme',   '2024-01-01 00:00:00+00'),
	('00000000-0000-0000-0000-00000000000b', 'globex', '2024-06-01 00:00:00+00');
INSERT INTO app.users VALUES
	('00000000-0000-0000-0000-00000000000a', 1, 'ada@acme.io',   'h1', NULL, 'happy', '{a,b}', '{"k": 1}', '{"k": 1}'),
	('00000000-0000-0000-0000-00000000000a', 2, 'bob@acme.io',   'h2', 1,    'ok',    '{}',    NULL,       NULL),
	('00000000-0000-0000-0000-00000000000a', 3, 'cy@ACME.io',    'h3', 1,    'sad',   NULL,    '[1, 2]',   '[1,2]'),
	('00000000-0000-0000-0000-00000000000b', 1, 'dee@globex.io', 'h4', NULL, NULL,    '{b}',   '{"k": 2}', NULL);
INSERT INTO app.posts SELECT g, '00000000-0000-0000-0000-00000000000a', 1 + g % 3, 'post ' || g FROM generate_series(1, 120) g;
INSERT INTO app.memberships VALUES
	('00000000-0000-0000-0000-00000000000a', 2, 'member'),
	('00000000-0000-0000-0000-00000000000b', 1, 'owner'),
	('00000000-0000-0000-0000-00000000000a', 1, 'owner');
INSERT INTO app.events SELECT generate_series(1, 10050);
INSERT INTO app.notes VALUES (1, 'short', 'short'), (2, repeat('b', 5000), 'short'), (3, repeat('b', 5000), repeat('t', 5000));
INSERT INTO app."we""ird; DROP TABLE app.orgs; --" VALUES (1, 'x'), (2, NULL);
ANALYZE;
`

func newFixture(t *testing.T) *fixture {
	t.Helper()
	testkit.Slow(t, "boots a real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	if _, err := admin.Exec(ctx, fixtureSQL); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Roles are cluster-wide on a shared server: name one per run.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	role := "browse_ro_" + hex.EncodeToString(b)
	for _, s := range []string{
		`CREATE ROLE %[1]s LOGIN PASSWORD 'pw'`,
		`GRANT USAGE ON SCHEMA app TO %[1]s`,
		`GRANT SELECT ON app.orgs, app.posts, app.events, app.notes, app.post_titles, app."we""ird; DROP TABLE app.orgs; --", schema_migrations TO %[1]s`,
		`GRANT SELECT (org_id, id, email, manager_id, mood, tags, meta, doc) ON app.users TO %[1]s`,
		`GRANT SELECT (id, note, org_id) ON app.sessions TO %[1]s`,
		`GRANT SELECT (user_id, role) ON app.memberships TO %[1]s`,
	} {
		if _, err := admin.Exec(ctx, fmt.Sprintf(s, role)); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %s`, role))
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, role))
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "pw")
	reader, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	t.Cleanup(func() { _ = reader.Close(ctx) })

	db, err := Describe(ctx, reader)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	return &fixture{admin: admin, reader: reader, db: db}
}

func (f *fixture) table(t *testing.T, name string) *Table {
	t.Helper()
	tb, err := f.db.Table(name)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func (f *fixture) browse(t *testing.T, req Request) *Page {
	t.Helper()
	c, err := Compile(f.db, req)
	if err != nil {
		t.Fatalf("Compile(%+v): %v", req, err)
	}
	p, err := Run(context.Background(), f.reader, c, dbquery.Options{})
	if err != nil {
		t.Fatalf("Run(%+v): %v\nSQL: %s", req, err, c.Page().SQL)
	}
	return p
}

func colNames(cols []Column) []string {
	var out []string
	for _, c := range cols {
		out = append(out, c.Name)
	}
	return out
}

// cells returns column i of every row, as fmt prints it.
func cells(p *Page, i int) []string {
	var out []string
	for _, r := range p.Rows {
		out = append(out, fmt.Sprint(r[i]))
	}
	return out
}

func TestDbbrowse(t *testing.T) {
	f := newFixture(t)

	t.Run("describe lists what the role can read", func(t *testing.T) {
		var got []string
		for _, tb := range f.db.Tables {
			got = append(got, tb.QualifiedName())
		}
		want := []string{"app.events", "app.memberships", "app.notes", "app.orgs", "app.post_titles", "app.posts", "app.sessions", "app.users", `app.we"ird; DROP TABLE app.orgs; --`}
		if !slices.Equal(got, want) {
			t.Errorf("tables = %q\nwant     %q", got, want)
		}

		orgs := f.table(t, "app.orgs")
		if orgs.Kind != KindTable || orgs.Comment != "tenants" || !slices.Equal(orgs.PrimaryKey, []string{"id"}) {
			t.Errorf("orgs = %+v", orgs)
		}
		users := f.table(t, "app.users")
		if got, want := colNames(users.Columns), []string{"org_id", "id", "email", "manager_id", "mood", "tags", "meta", "doc"}; !slices.Equal(got, want) {
			t.Errorf("users columns = %v, want %v (password_hash is ungranted)", got, want)
		}
		if _, ok := users.Column("password_hash"); ok {
			t.Error("an ungranted column is described")
		}
		email, _ := users.Column("email")
		if email.Type != "text" || email.Nullable || email.Comment != "login" || email.Ordinal != 3 {
			t.Errorf("email = %+v", email)
		}
		for name, want := range map[string]bool{"org_id": true, "mood": true, "tags": true, "meta": true, "doc": false} {
			if c, _ := users.Column(name); c.Sortable != want {
				t.Errorf("%s (%s): Sortable = %v, want %v", name, c.Type, c.Sortable, want)
			}
		}
		if c, _ := users.Column("mood"); c.Type != "app.mood" {
			t.Errorf("mood type = %q, want app.mood", c.Type)
		}
		if !slices.Equal(users.PrimaryKey, []string{"org_id", "id"}) {
			t.Errorf("users PK = %v", users.PrimaryKey)
		}

		// Outgoing: one single-column, one composite self-reference.
		out := map[string]ForeignKey{}
		for _, fk := range users.ForeignKeys {
			out[fk.Name] = fk
		}
		if fk := out["users_org_id_fkey"]; fk.RefTable != "orgs" || !slices.Equal(fk.Columns, []string{"org_id"}) || !slices.Equal(fk.RefColumns, []string{"id"}) || fk.OnDelete != "NO ACTION" {
			t.Errorf("users_org_id_fkey = %+v", fk)
		}
		if fk := out["users_manager_fkey"]; fk.RefTable != "users" || !slices.Equal(fk.Columns, []string{"org_id", "manager_id"}) || !slices.Equal(fk.RefColumns, []string{"org_id", "id"}) {
			t.Errorf("users_manager_fkey = %+v", fk)
		}
		if len(out) != 2 {
			t.Errorf("users foreign keys = %+v, want 2", users.ForeignKeys)
		}

		// Incoming: posts (composite, CASCADE) and the self-reference; not
		// audit (ungranted) or sessions (user_id ungranted).
		var in []string
		for _, fk := range users.ReferencedBy {
			in = append(in, fk.Table+"."+fk.Name)
			if fk.Name == "posts_author_fkey" && (fk.OnDelete != "CASCADE" || !slices.Equal(fk.Columns, []string{"org_id", "author_id"})) {
				t.Errorf("posts_author_fkey = %+v", fk)
			}
		}
		if want := []string{"posts.posts_author_fkey", "users.users_manager_fkey"}; !slices.Equal(in, want) {
			t.Errorf("users referenced by %v, want %v", in, want)
		}
		if refs := f.table(t, "app.orgs").ReferencedBy; len(refs) != 1 || refs[0].Table != "users" {
			t.Errorf("orgs referenced by %+v, want users", refs)
		}
		if s := f.table(t, "app.sessions"); len(s.ForeignKeys) != 0 || !slices.Equal(colNames(s.Columns), []string{"id", "org_id", "note"}) {
			t.Errorf("sessions = %+v", s)
		}

		view := f.table(t, "app.post_titles")
		if view.Kind != KindView || view.PrimaryKey != nil || view.EstimatedRows != -1 {
			t.Errorf("view = %+v", view)
		}
		if n := f.table(t, "app.events").EstimatedRows; n != 10050 {
			t.Errorf("events EstimatedRows = %d, want 10050", n)
		}
	})

	// audit is ungranted but references users, which the reader can read:
	// the server must not name it, its key or its columns to the reader at
	// all, not merely have Describe drop the row afterwards.
	t.Run("key metadata omits relations the role cannot inspect", func(t *testing.T) {
		rows, err := f.reader.Query(context.Background(), keysQuery)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var contype, name, schema, table, refSchema, refTable, del string
			var cols, refCols []string
			if err := rows.Scan(&contype, &name, &schema, &table, &refSchema, &refTable, &cols, &refCols, &del); err != nil {
				t.Fatal(err)
			}
			names = append(names, schema+"."+table+"."+name)
			if table == "audit" || refTable == "audit" {
				t.Errorf("keysQuery as the reader returned %s.%s.%s, on a relation it cannot inspect", schema, table, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(names, "app.posts.posts_author_fkey") {
			t.Errorf("keysQuery as the reader = %v, want posts_author_fkey among them", names)
		}
	})

	// memberships' key includes org_id, which the reader cannot read: the
	// key is not described, and browsing orders by the readable columns
	// instead of failing on a hidden one.
	t.Run("a key with an unreadable column does not order the page", func(t *testing.T) {
		m := f.table(t, "app.memberships")
		if m.PrimaryKey != nil {
			t.Errorf("memberships PK = %v, want none (org_id is ungranted)", m.PrimaryKey)
		}
		c, err := Compile(f.db, Request{Table: "app.memberships", Columns: []string{"role"}})
		if err != nil {
			t.Fatal(err)
		}
		if sql := c.Page().SQL; strings.Contains(sql, "org_id") {
			t.Errorf("SQL names the hidden key column: %s", sql)
		}
		p := f.browse(t, Request{Table: "app.memberships", Columns: []string{"role"}})
		if got, want := cells(p, 0), []string{"owner", "owner", "member"}; !slices.Equal(got, want) {
			t.Errorf("roles = %v, want %v (ordered by user_id, role)", got, want)
		}
	})

	t.Run("each operator", func(t *testing.T) {
		const acme = "00000000-0000-0000-0000-00000000000a"
		for _, tc := range []struct {
			filter Filter
			want   []string // emails, in PK order
		}{
			{Filter{"org_id", OpEq, []string{acme}}, []string{"ada@acme.io", "bob@acme.io", "cy@ACME.io"}},
			{Filter{"org_id", OpNeq, []string{acme}}, []string{"dee@globex.io"}},
			{Filter{"id", OpLt, []string{"2"}}, []string{"ada@acme.io", "dee@globex.io"}},
			{Filter{"id", OpLte, []string{"2"}}, []string{"ada@acme.io", "bob@acme.io", "dee@globex.io"}},
			{Filter{"id", OpGt, []string{"2"}}, []string{"cy@ACME.io"}},
			{Filter{"id", OpGte, []string{"2"}}, []string{"bob@acme.io", "cy@ACME.io"}},
			{Filter{"email", OpLike, []string{"%@acme.io"}}, []string{"ada@acme.io", "bob@acme.io"}},
			{Filter{"email", OpIlike, []string{"%@acme.io"}}, []string{"ada@acme.io", "bob@acme.io", "cy@ACME.io"}},
			{Filter{"manager_id", OpIsNull, nil}, []string{"ada@acme.io", "dee@globex.io"}},
			{Filter{"manager_id", OpNotNull, nil}, []string{"bob@acme.io", "cy@ACME.io"}},
			{Filter{"mood", OpIn, []string{"happy", "sad"}}, []string{"ada@acme.io", "cy@ACME.io"}},
			{Filter{"mood", OpGt, []string{"sad"}}, []string{"ada@acme.io", "bob@acme.io"}},
			{Filter{"tags", OpEq, []string{"{a,b}"}}, []string{"ada@acme.io"}},
			{Filter{"meta", OpEq, []string{`{"k":1}`}}, []string{"ada@acme.io"}},
			{Filter{"doc", OpEq, []string{`[1,2]`}}, []string{"cy@ACME.io"}},
			{Filter{"tags", OpLike, []string{"%b%"}}, []string{"ada@acme.io", "dee@globex.io"}},
		} {
			p := f.browse(t, Request{Table: "app.users", Columns: []string{"email"}, Filters: []Filter{tc.filter}})
			if got := cells(p, 0); !slices.Equal(got, tc.want) {
				t.Errorf("%s %s %q: %v, want %v", tc.filter.Column, tc.filter.Op, tc.filter.Values, got, tc.want)
			}
		}
		// A timestamptz value in another zone is still a timestamptz.
		p := f.browse(t, Request{Table: "app.orgs", Columns: []string{"name"}, Filters: []Filter{{"created_at", OpGt, []string{"2024-03-01T00:00:00-05:00"}}}})
		if got := cells(p, 0); !slices.Equal(got, []string{"globex"}) {
			t.Errorf("created_at > 2024-03-01: %v", got)
		}
	})

	t.Run("a value the type cannot read is the server's error", func(t *testing.T) {
		c, err := Compile(f.db, Request{Table: "app.users", Filters: []Filter{{"org_id", OpEq, []string{"not-a-uuid"}}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = Run(context.Background(), f.reader, c, dbquery.Options{})
		if dbquery.KindOf(err) != dbquery.KindSQL {
			t.Fatalf("err = %v (kind %q), want sql_error", err, dbquery.KindOf(err))
		}
	})

	t.Run("injection", func(t *testing.T) {
		for _, v := range []string{`x' OR '1'='1`, `'; DROP TABLE app.orgs; --`, `ada@acme.io' --`} {
			p := f.browse(t, Request{Table: "app.users", Filters: []Filter{{"email", OpEq, []string{v}}}})
			if len(p.Rows) != 0 {
				t.Errorf("value %q matched %d rows", v, len(p.Rows))
			}
		}
		// A hostile identifier the catalog really holds is quoted, never run.
		p := f.browse(t, Request{
			Table:   `app.we"ird; DROP TABLE app.orgs; --`,
			Filters: []Filter{{"col; --", OpNotNull, nil}},
			Sort:    []Sort{{Column: "col; --", Desc: true}},
		})
		if got := cells(p, 1); !slices.Equal(got, []string{"x"}) {
			t.Errorf("weird table rows = %v", got)
		}
		var n int
		if err := f.admin.QueryRow(context.Background(), `SELECT count(*) FROM app.orgs`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("app.orgs after injection attempts: %d rows, %v", n, err)
		}
		// The ungranted column cannot be asked for by name.
		if _, err := Compile(f.db, Request{Table: "app.users", Columns: []string{"password_hash"}}); !errors.Is(err, ErrUnknownColumn) {
			t.Errorf("password_hash: %v, want ErrUnknownColumn", err)
		}
		if _, err := Compile(f.db, Request{Table: "app.audit"}); !errors.Is(err, ErrUnknownTable) {
			t.Errorf("app.audit: %v, want ErrUnknownTable", err)
		}
	})

	t.Run("pagination", func(t *testing.T) {
		var seen []string
		for _, off := range []int{0, 50, 100} {
			p := f.browse(t, Request{Table: "app.posts", Columns: []string{"id"}, Limit: 50, Offset: off})
			want := off < 100
			if p.HasMore != want {
				t.Errorf("offset %d: HasMore = %v, want %v", off, p.HasMore, want)
			}
			seen = append(seen, cells(p, 0)...)
		}
		if len(seen) != 120 || seen[0] != "1" || seen[119] != "120" {
			t.Errorf("pages = %d rows, %v .. %v; want 1..120 in key order", len(seen), seen[0], seen[len(seen)-1])
		}
		// Ties in the requested sort break on the primary key.
		p := f.browse(t, Request{Table: "app.posts", Columns: []string{"author_id", "id"}, Sort: []Sort{{Column: "author_id", Desc: true}}, Limit: 3})
		if got := cells(p, 1); !slices.Equal(got, []string{"2", "5", "8"}) || cells(p, 0)[0] != "3" {
			t.Errorf("sorted page = %v", p.Rows)
		}
		// Exactly a full last page has no next page.
		if p := f.browse(t, Request{Table: "app.posts", Limit: 60, Offset: 60}); p.HasMore || len(p.Rows) != 60 {
			t.Errorf("last full page: HasMore %v, %d rows", p.HasMore, len(p.Rows))
		}
	})

	t.Run("counts", func(t *testing.T) {
		p := f.browse(t, Request{Table: "app.events", Limit: 1, Count: CountExact})
		if p.Total != ExactCountCap || !p.TotalAtLeast || p.TotalIsEstimate {
			t.Errorf("capped exact count = %+v", p)
		}
		p = f.browse(t, Request{Table: "app.events", Limit: 1, Count: CountExact, Filters: []Filter{{"id", OpLte, []string{"10"}}}})
		if p.Total != 10 || p.TotalAtLeast {
			t.Errorf("exact count of 10 = %d (at least %v)", p.Total, p.TotalAtLeast)
		}
		p = f.browse(t, Request{Table: "app.events", Limit: 1, Count: CountEstimated})
		if p.Total != 10050 || !p.TotalIsEstimate {
			t.Errorf("unfiltered estimate = %d (estimate %v)", p.Total, p.TotalIsEstimate)
		}
		p = f.browse(t, Request{Table: "app.events", Limit: 1, Count: CountEstimated, Filters: []Filter{{"id", OpGt, []string{"10000"}}}})
		if p.Total < 1 || p.Total > 1000 || !p.TotalIsEstimate {
			t.Errorf("filtered estimate = %d (estimate %v), want about 50", p.Total, p.TotalIsEstimate)
		}
		p = f.browse(t, Request{Table: "app.post_titles", Count: CountEstimated})
		if !p.TotalIsEstimate || p.Total < 1 {
			t.Errorf("view estimate = %d", p.Total)
		}
		if p := f.browse(t, Request{Table: "app.events", Limit: 1}); p.Total != -1 {
			t.Errorf("no count: Total = %d, want -1", p.Total)
		}
	})

	t.Run("truncated cells are reported per row for the rows shown", func(t *testing.T) {
		for _, tc := range []struct {
			limit int
			want  [][]int
			count int
		}{
			// Columns are id, body, title. The page statement fetches one
			// row past the limit; that row's cut cells are not the page's.
			{10, [][]int{{}, {1}, {1, 2}}, 3},
			{2, [][]int{{}, {1}}, 1},
			{1, nil, 0},
		} {
			p := f.browse(t, Request{Table: "app.notes", Limit: tc.limit})
			if !reflect.DeepEqual(p.TruncatedCells, tc.want) || p.CellsTruncated != tc.count {
				t.Errorf("limit %d: truncated_cells = %v (count %d), want %v (count %d)", tc.limit, p.TruncatedCells, p.CellsTruncated, tc.want, tc.count)
			}
			if p.TruncatedCells != nil && len(p.TruncatedCells) != len(p.Rows) {
				t.Errorf("limit %d: %d truncated_cells entries for %d rows", tc.limit, len(p.TruncatedCells), len(p.Rows))
			}
		}
	})

	t.Run("describe failures are typed", func(t *testing.T) {
		// Connections of their own: pgx closes one a canceled call was using.
		connect := func() *pgx.Conn {
			c, err := pgx.Connect(context.Background(), f.reader.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close(context.Background()) })
			return c
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Describe(ctx, connect()); dbquery.KindOf(err) != dbquery.KindCanceled {
			t.Errorf("canceled: err = %v (kind %q), want canceled", err, dbquery.KindOf(err))
		}
		closed := connect()
		_ = closed.Close(context.Background())
		if _, err := Describe(context.Background(), closed); dbquery.KindOf(err) != dbquery.KindUnavailable {
			t.Errorf("closed connection: err = %v (kind %q), want unavailable", err, dbquery.KindOf(err))
		}
	})

	t.Run("the auditor sees every statement with its args", func(t *testing.T) {
		var events []dbquery.Event
		c, err := Compile(f.db, Request{Table: "app.users", Filters: []Filter{{"email", OpEq, []string{"ada@acme.io"}}}, Count: CountExact})
		if err != nil {
			t.Fatal(err)
		}
		_, err = Run(context.Background(), f.reader, c, dbquery.Options{Auditor: dbquery.AuditFunc(func(_ context.Context, e dbquery.Event) error {
			events = append(events, e)
			return nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || !strings.HasPrefix(events[1].SQL, "SELECT count(*)") {
			t.Fatalf("events = %+v", events)
		}
		for _, e := range events {
			if e.Outcome != "ok" || len(e.Args) != 1 || e.Args[0] != "ada@acme.io" {
				t.Errorf("event %q: outcome %s args %v", e.SQL, e.Outcome, e.Args)
			}
		}
	})
}
