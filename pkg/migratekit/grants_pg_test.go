package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// The read-only role's grants are a post-step of every migration run
// (grants.go). These tests drive the real migrator against real Postgres and
// then ACT AS the role, because the claim is Postgres's: a secret column is
// refused by the server, not hidden by Go.
//
// Roles are cluster-wide and the test server is shared with every other test
// process on the machine, so each test uses a role of its own (useRole) and
// drops it again. None of them calls t.Parallel: they swap the package's
// grantsRole.

const grantsDir = "grants"

const (
	usersSQL = "CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL, api_token TEXT NOT NULL DEFAULT '');"
	notesSQL = "CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL);"
)

// grantsRelease is one binary's embedded set: migrations plus the grants file
// the generator projected for them.
func grantsRelease(migrations map[string]string, grants string) fstest.MapFS {
	fsys := release(migrations)
	if grants != "" {
		fsys[grantsDir+"/readonly_gen.sql"] = &fstest.MapFile{Data: []byte(grants)}
	}
	return fsys
}

// testRole is the role the current test uses. See useRole.
var testRole string

func mustRender(t *testing.T, tables ...dbgrants.Table) string {
	t.Helper()
	script, err := dbgrants.RenderFor(testRole, tables)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func usersTable(secretToken bool) dbgrants.Table {
	return dbgrants.Table{Schema: "public", Name: "users", Columns: []dbgrants.Column{
		{Name: "id"}, {Name: "email"}, {Name: "api_token", Secret: secretToken, Readable: !secretToken},
	}}
}

var notesTable = dbgrants.Table{Schema: "public", Name: "notes", Columns: []dbgrants.Column{{Name: "id"}, {Name: "body"}}}

// useRole points the migrator at a role no other test process can share and
// returns its name. The role is not created: withRole does that.
func useRole(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("forge_ro_t%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	prevRole, prevGrants := testRole, grantsRole
	testRole, grantsRole = name, name
	t.Cleanup(func() { testRole, grantsRole = prevRole, prevGrants })
	return name
}

// withRole creates the test's read-only role, with LOGIN and no other
// attribute, and drops it at the end.
func withRole(t *testing.T, dsn string) {
	t.Helper()
	admin := openRecordDB(t, dsn)
	ctx := context.Background()
	if testRole == "" {
		useRole(t)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE`, testRole)); err != nil {
		t.Fatalf("create role: %v", err)
	}
	role := testRole
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.ExecContext(c, `DROP OWNED BY `+role)
		_, _ = admin.ExecContext(c, `DROP ROLE IF EXISTS `+role)
	})
}

// asRole runs fn on a session that has become forge_ro. SET ROLE is how a test
// becomes the role without a password; the privilege checks are identical.
func asRole(t *testing.T, dsn string, fn func(ctx context.Context, conn *sql.Conn)) {
	t.Helper()
	db := openRecordDB(t, dsn)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+testRole); err != nil {
		t.Fatalf("become %s: %v", testRole, err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `RESET ROLE`) }()
	fn(ctx, conn)
}

func readable(ctx context.Context, conn *sql.Conn, query string) error {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	return rows.Err()
}

func wantDenied(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: succeeded, want permission denied", what)
		return
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("%s: %v, want permission denied", what, err)
	}
}

func seed(t *testing.T, dsn string) {
	t.Helper()
	db := openRecordDB(t, dsn)
	for _, q := range []string{
		`INSERT INTO users (id, email, api_token) VALUES ('u1', 'a@example.test', 'tok-SECRET-1')`,
		`INSERT INTO notes (id, body) VALUES ('n1', 'hello')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

// TestGrantsReadOrdinaryColumnsAndRefuseSecretOnes: after Up, the role selects
// what the grants list, and Postgres refuses the secret column however it is
// reached.
func TestGrantsReadOrdinaryColumnsAndRefuseSecretOnes(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	grants := mustRender(t, usersTable(true), notesTable)

	res := mustUp(t, grantsRelease(map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}, grants), dsn)
	if res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("Result.Grants = %+v, want applied", res.Grants)
	}
	seed(t, dsn)

	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		for _, q := range []string{
			`SELECT id, email FROM users`,
			`SELECT id, body FROM notes`,
			`SELECT count(*) FROM users`,
			`SELECT u.email, n.body FROM users u CROSS JOIN notes n`,
		} {
			if err := readable(ctx, c, q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		for _, q := range []string{
			`SELECT api_token FROM users`,
			`SELECT * FROM users`,
			`SELECT upper(api_token) FROM users`,
			`SELECT id FROM users WHERE api_token <> ''`,
			`SELECT count(*) FROM users GROUP BY api_token`,
			`SELECT id FROM users ORDER BY api_token`,
			`SELECT users FROM users`,
			`SELECT row_to_json(users) FROM users`,
			`SELECT users.* FROM users`,
			`SELECT u FROM users u`,
			`SELECT to_jsonb(u) FROM users u`,
			`WITH x AS (SELECT * FROM users) SELECT id FROM x`,
		} {
			wantDenied(t, readable(ctx, c, q), q)
		}
	})
}

// TestGrantsGiveNoWritePrivilege: the role has no INSERT, UPDATE, DELETE or
// TRUNCATE on anything, no sequence, and cannot create objects. The grants are
// the boundary; the read-only transaction dbquery opens is a second one.
func TestGrantsGiveNoWritePrivilege(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	grants := mustRender(t, usersTable(true), notesTable)
	mustUp(t, grantsRelease(map[string]string{
		"1_users.up.sql": usersSQL,
		"2_notes.up.sql": notesSQL + "\nCREATE SEQUENCE ticket_seq;",
	}, grants), dsn)
	seed(t, dsn)

	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		for _, q := range []string{
			`INSERT INTO notes (id, body) VALUES ('n2', 'x')`,
			`INSERT INTO users (id, email) VALUES ('u2', 'b@example.test')`,
			`UPDATE notes SET body = 'x'`,
			`UPDATE users SET email = 'x'`,
			`DELETE FROM notes`,
			`DELETE FROM users`,
			`TRUNCATE notes`,
			`SELECT nextval('ticket_seq')`,
			`SELECT last_value FROM ticket_seq`,
			`CREATE TABLE evil (id int)`,
			`CREATE SCHEMA evil`,
			`COPY users TO STDOUT`,
			`COPY notes FROM STDIN`,
		} {
			_, err := c.ExecContext(ctx, q)
			wantDenied(t, err, q)
		}
		// A read is not a write: COPY of a granted table is a SELECT.
		if _, err := c.ExecContext(ctx, `COPY notes TO STDOUT`); err != nil {
			t.Errorf("COPY of a granted table is a read: %v", err)
		}
		// TEMPORARY on a database is granted to PUBLIC by Postgres and cannot be
		// taken from one role. A temp table is session-local and the read-only
		// transaction refuses it; what matters here is that it persists nothing.
		_, _ = c.ExecContext(ctx, `CREATE TEMP TABLE scratch AS SELECT id FROM notes`)
	})

	// Nothing leaked through the attempts above.
	db := openRecordDB(t, dsn)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 1 {
		t.Errorf("notes rows = %d (err %v), want the one seeded row", n, err)
	}

	// The catalog says the same thing the attempts did: the role holds NO
	// table-level privilege at all (a table-level SELECT would cover a column
	// added later, secret or not) and no sequence privilege; its column-level
	// privileges are SELECT and nothing else.
	rows, err := db.Query(`
		SELECT c.relkind::text, c.relname, a.privilege_type
		  FROM pg_class c, LATERAL aclexplode(c.relacl) a
		 WHERE a.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)`, testRole)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, rel, priv string
		if err := rows.Scan(&kind, &rel, &priv); err != nil {
			t.Fatal(err)
		}
		t.Errorf("the role holds %s on %s %s directly: only column privileges are granted", priv, kind, rel)
	}
	_ = rows.Close()
	cols, err := db.Query(`
		SELECT c.relname, a.attname, p.privilege_type
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid, LATERAL aclexplode(a.attacl) p
		 WHERE p.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)
		   AND p.privilege_type <> 'SELECT'`, testRole)
	if err != nil {
		t.Fatal(err)
	}
	for cols.Next() {
		var rel, col, priv string
		if err := cols.Scan(&rel, &col, &priv); err != nil {
			t.Fatal(err)
		}
		t.Errorf("the role holds %s on %s.%s: column grants are SELECT only", priv, rel, col)
	}
	_ = cols.Close()
	var tableSelect bool
	if err := db.QueryRow(`SELECT has_table_privilege($1, 'users', 'SELECT')`, testRole).Scan(&tableSelect); err != nil {
		t.Fatal(err)
	}
	if tableSelect {
		t.Error("the role has table-level SELECT on users: a column added later would be readable, secret or not")
	}
}

// TestGrantsCoverANewTableAfterAMigration: grants are re-applied after every
// run, so a table created by a later migration is readable once the regenerated
// grants ship with it.
func TestGrantsCoverANewTableAfterAMigration(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	first := map[string]string{"1_users.up.sql": usersSQL}
	mustUp(t, grantsRelease(first, mustRender(t, usersTable(true))), dsn)
	seed2 := openRecordDB(t, dsn)
	if _, err := seed2.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'a@example.test')`); err != nil {
		t.Fatal(err)
	}

	second := map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}
	res := mustUp(t, grantsRelease(second, mustRender(t, usersTable(true), notesTable)), dsn)
	if !res.Changed || res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("second run: Changed=%v Grants=%+v, want a migration applied and grants applied", res.Changed, res.Grants)
	}
	if _, err := seed2.Exec(`INSERT INTO notes (id, body) VALUES ('n1', 'hello')`); err != nil {
		t.Fatal(err)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, body FROM notes`); err != nil {
			t.Errorf("new table not granted: %v", err)
		}
		wantDenied(t, readable(ctx, c, `SELECT api_token FROM users`), "secret column of the old table")
	})
}

// TestGrantsKeepANewSecretColumnOut: a column added later and marked secret is
// never readable, and a column added later and NOT marked is readable only once
// the regenerated grants name it, never by default.
func TestGrantsKeepANewSecretColumnOut(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	base := map[string]string{"1_users.up.sql": usersSQL}
	mustUp(t, grantsRelease(base, mustRender(t, usersTable(true))), dsn)

	next := map[string]string{
		"1_users.up.sql": usersSQL,
		"2_more.up.sql":  "ALTER TABLE users ADD COLUMN recovery_code TEXT NOT NULL DEFAULT 'rc-SECRET'; ALTER TABLE users ADD COLUMN nickname TEXT NOT NULL DEFAULT 'n';",
	}

	// Stale grants (the migration landed; generate has not run): the new columns
	// are not readable. Nothing is granted by default.
	mustUp(t, grantsRelease(next, mustRender(t, usersTable(true))), dsn)
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		wantDenied(t, readable(ctx, c, `SELECT nickname FROM users`), "new column before regenerate")
		wantDenied(t, readable(ctx, c, `SELECT recovery_code FROM users`), "new column before regenerate")
		if err := readable(ctx, c, `SELECT id, email FROM users`); err != nil {
			t.Errorf("existing granted columns: %v", err)
		}
	})

	// Regenerated grants: nickname is ordinary, recovery_code is forge:secret.
	table := dbgrants.Table{Schema: "public", Name: "users", Columns: []dbgrants.Column{
		{Name: "id"}, {Name: "email"}, {Name: "api_token", Secret: true},
		{Name: "recovery_code", Secret: true}, {Name: "nickname"},
	}}
	res := mustUp(t, grantsRelease(next, mustRender(t, table)), dsn)
	if res.Changed || res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("re-run: Changed=%v Grants=%+v, want nothing pending and the grants re-applied", res.Changed, res.Grants)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, email, nickname FROM users`); err != nil {
			t.Errorf("ordinary new column: %v", err)
		}
		wantDenied(t, readable(ctx, c, `SELECT recovery_code FROM users`), "new secret column")
		wantDenied(t, readable(ctx, c, `SELECT api_token FROM users`), "old secret column")
		wantDenied(t, readable(ctx, c, `SELECT * FROM users`), "star over a table with secret columns")
	})
}

// TestGrantsRevokeWhenAColumnBecomesSecret: a column that WAS readable and is
// now marked secret loses its grant. The script is a convergence, not a delta.
func TestGrantsRevokeWhenAColumnBecomesSecret(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	migrations := map[string]string{"1_users.up.sql": usersSQL}
	mustUp(t, grantsRelease(migrations, mustRender(t, usersTable(false))), dsn)
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT api_token FROM users`); err != nil {
			t.Fatalf("before the marker: %v", err)
		}
	})
	mustUp(t, grantsRelease(migrations, mustRender(t, usersTable(true))), dsn)
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		wantDenied(t, readable(ctx, c, `SELECT api_token FROM users`), "column marked forge:secret")
		if err := readable(ctx, c, `SELECT id, email FROM users`); err != nil {
			t.Errorf("columns that stayed ordinary: %v", err)
		}
	})
}

// TestGrantsAreIdempotent: applying the same grants twice changes nothing and
// fails nothing, and the privileges are identical.
func TestGrantsAreIdempotent(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	fsys := grantsRelease(map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}, mustRender(t, usersTable(true), notesTable))
	snapshot := func() string {
		db := openRecordDB(t, dsn)
		rows, err := db.Query(`
			SELECT table_name || '.' || column_name || ':' || privilege_type
			  FROM information_schema.column_privileges
			 WHERE grantee = $1 AND table_schema = 'public'
			 ORDER BY 1`, testRole)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, ",")
	}
	mustUp(t, fsys, dsn)
	first := snapshot()
	if first == "" {
		t.Fatal("no privileges after the first run")
	}
	for i := 0; i < 3; i++ {
		res := mustUp(t, fsys, dsn)
		if res.Changed || res.Grants == nil || !res.Grants.Applied {
			t.Fatalf("run %d: Changed=%v Grants=%+v", i+2, res.Changed, res.Grants)
		}
		if got := snapshot(); got != first {
			t.Fatalf("run %d changed the privileges:\n got %s\nwant %s", i+2, got, first)
		}
	}
	if strings.Contains(first, "api_token") {
		t.Errorf("secret column granted: %s", first)
	}
}

// TestGrantsAreANoOpWithoutTheRole: a database nobody provisioned forge_ro for
// (a local compose Postgres) migrates as it always did, and says so.
func TestGrantsAreANoOpWithoutTheRole(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	res := mustUp(t, grantsRelease(map[string]string{"1_users.up.sql": usersSQL}, mustRender(t, usersTable(true))), dsn)
	if res.Grants == nil || !res.Grants.RoleMissing || res.Grants.Applied {
		t.Fatalf("Result.Grants = %+v, want RoleMissing and not applied", res.Grants)
	}
}

// TestGrantsCatchUpWhenTheRoleAppearsAfterTheMigration: CloudNativePG creates a
// managed role when it reconciles the Cluster, which can be after the first
// migration Job. The next run, with nothing pending, applies the grants.
func TestGrantsCatchUpWhenTheRoleAppearsAfterTheMigration(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	fsys := grantsRelease(map[string]string{"1_users.up.sql": usersSQL}, mustRender(t, usersTable(true)))
	if res := mustUp(t, fsys, dsn); res.Grants == nil || !res.Grants.RoleMissing {
		t.Fatalf("first run: Grants = %+v, want RoleMissing", res.Grants)
	}
	withRole(t, dsn)
	res := mustUp(t, fsys, dsn)
	if res.Changed || res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("second run: Changed=%v Grants=%+v, want nothing pending and the grants applied", res.Changed, res.Grants)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, email FROM users`); err != nil {
			t.Errorf("granted after the role appeared: %v", err)
		}
	})
}

// TestGrantsFileNamingAMissingTableOrColumnDoesNotFailTheRun: a stale file (a
// table or column dropped since `forge generate`) must not fail the migration
// that drops it, or the deploy that ships the drop could never succeed.
func TestGrantsFileNamingAMissingTableOrColumnDoesNotFailTheRun(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	stale := mustRender(t, usersTable(true), notesTable, dbgrants.Table{
		Schema: "public", Name: "dropped_table", Columns: []dbgrants.Column{{Name: "id"}},
	}, dbgrants.Table{
		Schema: "elsewhere", Name: "nope", Columns: []dbgrants.Column{{Name: "id"}},
	})
	// users has no `ghost` column in the schema, the grants name one.
	stale = strings.Replace(stale, "'email'", "'email', 'ghost'", 1)
	res := mustUp(t, grantsRelease(map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}, stale), dsn)
	if res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("Grants = %+v, want applied", res.Grants)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, email FROM users`); err != nil {
			t.Error(err)
		}
	})
}

// TestGrantsAreNotAppliedWhenTheSchemaIsAheadOfTheBinary: an older release
// meeting a newer schema must not rewrite the grants from its older view.
func TestGrantsAreNotAppliedWhenTheSchemaIsAheadOfTheBinary(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	newer := map[string]string{
		"1_users.up.sql": usersSQL,
		"2_notes.up.sql": notesSQL,
		"3_more.up.sql":  "-- forge:backward-compatible\nALTER TABLE notes ADD COLUMN pinned BOOL NOT NULL DEFAULT false;",
	}
	newerTable := dbgrants.Table{Schema: "public", Name: "notes", Columns: []dbgrants.Column{{Name: "id"}, {Name: "body"}, {Name: "pinned"}}}
	mustUp(t, grantsRelease(newer, mustRender(t, usersTable(true), newerTable)), dsn)

	older := map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}
	res := mustUp(t, grantsRelease(older, mustRender(t, usersTable(true), notesTable)), dsn)
	if res.Ahead == nil {
		t.Fatalf("expected the older binary to see the schema ahead, got %+v", res)
	}
	if res.Grants != nil {
		t.Errorf("Grants = %+v, want nil: an older binary must not converge the grants", res.Grants)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT pinned FROM notes`); err != nil {
			t.Errorf("the newer release's grant was revoked by the older binary: %v", err)
		}
	})
}

// TestGrantsAcrossSchemas: a table in a non-public schema is covered, and the
// role is given USAGE on that schema.
func TestGrantsAcrossSchemas(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	sqlText := "CREATE SCHEMA billing; CREATE TABLE billing.invoices (id TEXT PRIMARY KEY, total INT NOT NULL, card_number TEXT NOT NULL DEFAULT '');"
	grants := mustRender(t, dbgrants.Table{Schema: "billing", Name: "invoices", Columns: []dbgrants.Column{
		{Name: "id"}, {Name: "total"}, {Name: "card_number", Secret: true},
	}})
	mustUp(t, grantsRelease(map[string]string{"1_billing.up.sql": sqlText}, grants), dsn)
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, total FROM billing.invoices`); err != nil {
			t.Errorf("non-public schema: %v", err)
		}
		wantDenied(t, readable(ctx, c, `SELECT card_number FROM billing.invoices`), "secret column in billing")
	})
}

// TestGrantsFileIsOptional: a project that embeds no grants migrates exactly as
// before, with no Result.Grants.
func TestGrantsFileIsOptional(t *testing.T) {
	dsn := requirePG(t)
	res := mustUp(t, release(map[string]string{"1_users.up.sql": usersSQL}), dsn)
	if res.Grants != nil {
		t.Errorf("Grants = %+v, want nil when the project embeds none", res.Grants)
	}
}

// TestApplyAppliesTheGrantsToo: the boot path (AutoMigrate -> Apply) converges
// the grants the same way `db migrate up` does.
func TestApplyAppliesTheGrantsToo(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	db := openRecordDB(t, dsn)
	fsys := grantsRelease(map[string]string{"1_users.up.sql": usersSQL}, mustRender(t, usersTable(true)))
	res, err := Apply(context.Background(), db, fsys, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("Apply: Grants = %+v, want applied", res.Grants)
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		wantDenied(t, readable(ctx, c, fmt.Sprintf(`SELECT %s FROM users`, "api_token")), "secret column through Apply")
		if err := readable(ctx, c, `SELECT id FROM users`); err != nil {
			t.Error(err)
		}
	})
}
