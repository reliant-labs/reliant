package codegen

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The read-only role must be safe by construction: a credential-shaped column
// that nobody decided is refused BY POSTGRES, a forge:readable one is granted,
// and a forge:secret one declared in SQL is withheld even though no proto field
// exists to carry the marker. These drive the production chain end to end —
// migrations with COMMENT ON COLUMN, the introspected catalog,
// ReadOnlyGrantTables, dbgrants.Render, Apply — and then ACT AS the role.

const grantsSchemaSQL = `
CREATE TABLE git_credentials (
    id            TEXT PRIMARY KEY,
    provider      TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_hash    TEXT NOT NULL,
    note          TEXT NOT NULL,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    blob          BYTEA NOT NULL DEFAULT ''::bytea
);
INSERT INTO git_credentials VALUES ('g1', 'github', 'ghp-SECRET-1', 'ghr-SECRET-2', 'h-1', 'a note', 7, '\x01');
`

// roleFixture applies the schema, optionally followed by more migration text,
// creates a role of this test's own (roles are cluster-wide and the server is
// shared), converges its grants from the production projection, and runs fn as
// that role.
func roleFixture(t *testing.T, more string, fn func(ctx context.Context, c *sql.Conn)) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; runs in task test")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	for _, stmt := range []string{grantsSchemaSQL, more} {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}

	role := fmt.Sprintf("forge_ro_cg%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	if _, err := db.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP OWNED BY `+role)
		_, _ = db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+role)
	})

	tables, err := schemadef.Introspect(db)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if err := schemadef.CheckMarkers(tables); err != nil {
		t.Fatalf("CheckMarkers: %v", err)
	}
	script, err := dbgrants.RenderFor(role, ReadOnlyGrantTables(tables, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := dbgrants.Apply(ctx, db, script); err != nil {
		t.Fatalf("apply grants: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+role); err != nil {
		t.Fatalf("become %s: %v", role, err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `RESET ROLE`) }()
	fn(ctx, conn)
}

func canSelect(ctx context.Context, c *sql.Conn, query string) error {
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	return rows.Err()
}

func mustBeDenied(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: succeeded, want permission denied", what)
		return
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("%s: %v, want permission denied", what, err)
	}
}

// With no declaration at all, every credential-shaped column is refused: the
// role is safe by omission. Ordinary columns, and a numeric counter that merely
// has "token" in its name, stay readable.
func TestReadOnlyRole_UndecidedCredentialColumnsAreNotReadable(t *testing.T) {
	roleFixture(t, "", func(ctx context.Context, c *sql.Conn) {
		for _, q := range []string{
			`SELECT id, provider, note FROM git_credentials`,
			`SELECT input_tokens FROM git_credentials`,
			`SELECT count(*) FROM git_credentials`,
		} {
			if err := canSelect(ctx, c, q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		for _, col := range []string{"access_token", "refresh_token", "token_hash", "blob"} {
			mustBeDenied(t, canSelect(ctx, c, `SELECT `+col+` FROM git_credentials`), "undecided "+col)
			mustBeDenied(t, canSelect(ctx, c, `SELECT upper(`+col+`::text) FROM git_credentials`), "undecided "+col+" through an expression")
		}
		mustBeDenied(t, canSelect(ctx, c, `SELECT * FROM git_credentials`), "star over a table with undecided columns")
		mustBeDenied(t, canSelect(ctx, c, `SELECT row_to_json(g) FROM git_credentials g`), "row_to_json over a table with undecided columns")
	})
}

// forge:readable <reason> is the explicit decision that grants a
// credential-shaped column; it grants that column and no other.
func TestReadOnlyRole_ForgeReadableGrantsTheColumn(t *testing.T) {
	roleFixture(t, `
COMMENT ON COLUMN git_credentials.token_hash IS 'forge:readable — content hash of the clone URL, not of a credential';
COMMENT ON COLUMN git_credentials.blob IS 'forge:readable — opaque attachment, no secret';
`, func(ctx context.Context, c *sql.Conn) {
		for _, q := range []string{
			`SELECT token_hash FROM git_credentials`,
			`SELECT blob FROM git_credentials`,
			`SELECT id, token_hash, note FROM git_credentials`,
		} {
			if err := canSelect(ctx, c, q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		mustBeDenied(t, canSelect(ctx, c, `SELECT access_token FROM git_credentials`), "access_token, still undecided")
		mustBeDenied(t, canSelect(ctx, c, `SELECT refresh_token FROM git_credentials`), "refresh_token, still undecided")
	})
}

// forge:secret declared in SQL withholds a column that has no wire field to
// carry the proto marker, including one whose name gives it away to nothing.
func TestReadOnlyRole_ForgeSecretInSQLExcludesTheColumn(t *testing.T) {
	roleFixture(t, `
ALTER TABLE git_credentials ADD COLUMN session_blob TEXT NOT NULL DEFAULT 'sess-SECRET';
ALTER TABLE git_credentials ADD COLUMN value TEXT NOT NULL DEFAULT 'val-SECRET';
COMMENT ON COLUMN git_credentials.access_token IS 'forge:secret';
COMMENT ON COLUMN git_credentials.value IS 'forge:secret — an arbitrary credential stored under an innocent name';
COMMENT ON COLUMN git_credentials.token_hash IS 'forge:readable — not a credential';
COMMENT ON COLUMN git_credentials.session_blob IS 'forge:secret';
`, func(ctx context.Context, c *sql.Conn) {
		for _, col := range []string{"access_token", "value", "session_blob"} {
			mustBeDenied(t, canSelect(ctx, c, `SELECT `+col+` FROM git_credentials`), "forge:secret "+col)
		}
		if err := canSelect(ctx, c, `SELECT id, provider, token_hash, note FROM git_credentials`); err != nil {
			t.Errorf("the rest of the table must stay readable: %v", err)
		}
	})
}

// A column declared both ways is hidden: secret wins, so a stray readable never
// un-hides something somebody marked.
func TestReadOnlyRole_SecretWinsOverReadable(t *testing.T) {
	roleFixture(t, `
COMMENT ON COLUMN git_credentials.access_token IS 'forge:secret forge:readable — contradictory on purpose';
`, func(ctx context.Context, c *sql.Conn) {
		mustBeDenied(t, canSelect(ctx, c, `SELECT access_token FROM git_credentials`), "a column declared both secret and readable")
	})
}

// The lint's list and the grants' list come from one function, so a column the
// lint reports undecided is exactly one the role cannot read, and a decision
// removes it from both.
func TestUndecidedReadOnlyColumns_AgreesWithTheGrants(t *testing.T) {
	cols := func(extraComment map[string]string) []schemadef.Column {
		var out []schemadef.Column
		for _, n := range []string{"id", "access_token", "token_hash", "input_tokens", "password"} {
			c := schemadef.Column{Name: n, Type: schemadef.TypeString, TypeKnown: true, Comment: extraComment[n]}
			if n == "input_tokens" {
				c.Type = schemadef.TypeInt
			}
			out = append(out, c)
		}
		return out
	}
	table := func(m map[string]string) []schemadef.Table {
		return []schemadef.Table{{Name: "creds", Schema: "public", Columns: cols(m)}}
	}

	var got []string
	for _, f := range UndecidedReadOnlyColumns(table(nil), nil) {
		got = append(got, f.Column)
	}
	if want := "access_token,password,token_hash"; strings.Join(got, ",") != want {
		t.Errorf("undecided = %v, want %s (input_tokens is a counter, id is plain)", got, want)
	}

	decided := table(map[string]string{
		"access_token": "forge:secret",
		"token_hash":   "forge:readable — not a credential",
		"password":     "forge:secret",
	})
	if left := UndecidedReadOnlyColumns(decided, nil); len(left) != 0 {
		t.Errorf("decided columns still reported: %v", left)
	}
	grant := ReadOnlyGrantTables(decided, nil)[0]
	for _, c := range grant.Columns {
		wantWithheld := c.Name == "access_token" || c.Name == "password"
		if withheld := c.Disposition() != dbgrants.Granted; withheld != wantWithheld {
			t.Errorf("column %s withheld=%v, want %v", c.Name, withheld, wantWithheld)
		}
	}
}
