package dbgrants

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests drive RenderFor against real Postgres and then ACT AS the role.
// The claim is Postgres's: a privilege the declaration no longer names is gone
// after the next run, whichever way it was granted. They boot a server, so they
// skip under -short and run in task test.

type pgFixture struct {
	t    *testing.T
	db   *sql.DB
	role string
}

func newPGFixture(t *testing.T, schemaSQL string) *pgFixture {
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
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	// Roles are cluster-wide and the server is shared with every other test
	// process, so each fixture owns a role of its own.
	role := fmt.Sprintf("forge_dg%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	if _, err := db.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP OWNED BY `+role)
		_, _ = db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+role)
	})
	return &pgFixture{t: t, db: db, role: role}
}

func (f *pgFixture) exec(q string) {
	f.t.Helper()
	if _, err := f.db.ExecContext(context.Background(), q); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *pgFixture) converge(tables []Table) {
	f.t.Helper()
	script, err := RenderFor(f.role, tables)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := Apply(context.Background(), f.db, script); err != nil {
		f.t.Fatalf("apply: %v\n%s", err, script)
	}
}

// try runs q as the role and returns its error.
func (f *pgFixture) try(q string) error {
	f.t.Helper()
	ctx := context.Background()
	conn, err := f.db.Conn(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+f.role); err != nil {
		f.t.Fatalf("become %s: %v", f.role, err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `RESET ROLE`) }()
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	return rows.Err()
}

func (f *pgFixture) allowed(q, what string) {
	f.t.Helper()
	if err := f.try(q); err != nil {
		f.t.Errorf("%s: %s: %v, want allowed", what, q, err)
	}
}

func (f *pgFixture) denied(q, what string) {
	f.t.Helper()
	err := f.try(q)
	if err == nil {
		f.t.Errorf("%s: %s succeeded, want permission denied", what, q)
		return
	}
	if !strings.Contains(err.Error(), "permission denied") {
		f.t.Errorf("%s: %s: %v, want permission denied", what, q, err)
	}
}

// privilegeRows counts every row the catalog holds for the role: table-level
// and column-level, via information_schema, which is the same view
// has_*_privilege answers from.
func (f *pgFixture) privilegeRows(what string) int {
	f.t.Helper()
	var n int
	err := f.db.QueryRowContext(context.Background(), `
SELECT (SELECT count(*) FROM information_schema.table_privileges  WHERE grantee = $1)
     + (SELECT count(*) FROM information_schema.column_privileges WHERE grantee = $1)`, f.role).Scan(&n)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

const convergeSchemaSQL = `
CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    model      TEXT NOT NULL,
    user_email TEXT NOT NULL,
    note       TEXT NOT NULL
);
INSERT INTO events VALUES ('e1', 'sonnet', 'a@example.test', 'free text');
CREATE TABLE other (id TEXT PRIMARY KEY, body TEXT NOT NULL);
INSERT INTO other VALUES ('o1', 'b');
`

func eventsTable(cols ...Column) []Table {
	return []Table{
		{Schema: "public", Name: "events", Columns: cols},
		{Schema: "public", Name: "other"},
	}
}

// A column that stops being declared stops being readable on the next run: the
// previous run's column grant must not survive the new declaration.
func TestConvergeRevokesAColumnNoLongerDeclared(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.converge(eventsTable(Column{Name: "id"}, Column{Name: "model"}, Column{Name: "user_email"}))
	f.allowed(`SELECT user_email FROM events`, "declared column")

	f.converge(eventsTable(Column{Name: "id"}, Column{Name: "model"}))
	f.denied(`SELECT user_email FROM events`, "column dropped from the declaration")
	f.allowed(`SELECT id, model FROM events`, "columns still declared")
}

// The same, when the column became secret instead of being undeclared.
func TestConvergeRevokesAColumnThatBecameSecret(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.converge(eventsTable(Column{Name: "id"}, Column{Name: "user_email"}))
	f.allowed(`SELECT user_email FROM events`, "before it became secret")

	f.converge(eventsTable(Column{Name: "id"}, Column{Name: "user_email", Secret: true}))
	f.denied(`SELECT user_email FROM events`, "newly secret column")
	f.denied(`SELECT upper(user_email) FROM events`, "newly secret column through an expression")
}

// Every column of a table that left the declaration's readable set goes: the
// marker-removal case, where the table stays declared with no columns.
func TestConvergeRevokesATableWhoseColumnsAllLeft(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.converge(eventsTable(Column{Name: "id"}, Column{Name: "model"}))
	f.allowed(`SELECT model FROM events`, "declared")

	f.converge(eventsTable())
	f.denied(`SELECT model FROM events`, "a table with no declared column")
	f.denied(`SELECT id FROM events`, "a table with no declared column")
	if n := f.privilegeRows("after"); n != 0 {
		t.Errorf("%d privilege rows remain for the role, want 0", n)
	}
}

// A column privilege that a previous run never made: granted by hand, or by an
// older forge, or on a column the declaration has never heard of. The role must
// hold exactly what the declaration lists.
func TestConvergeRevokesColumnGrantsTheDeclarationNeverNamed(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role)
	f.exec(`GRANT SELECT (user_email, note), UPDATE (note), INSERT (note), REFERENCES (id) ON events TO ` + f.role)
	f.allowed(`SELECT user_email FROM events`, "hand-granted before convergence")

	f.converge(eventsTable(Column{Name: "id"}))
	f.denied(`SELECT user_email FROM events`, "hand-granted column")
	f.denied(`SELECT note FROM events`, "hand-granted column")
	f.denied(`UPDATE events SET note = 'x'`, "hand-granted column UPDATE")
	f.denied(`INSERT INTO events (note) VALUES ('x')`, "hand-granted column INSERT")
	f.allowed(`SELECT id FROM events`, "the declared column")
	var refs int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM information_schema.column_privileges WHERE grantee = $1 AND privilege_type <> 'SELECT'`,
		f.role).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Errorf("%d non-SELECT column privileges remain", refs)
	}
}

// Privileges on objects other than a declared table's columns: every one is
// revoked by the next run, whatever granted it.
func TestConvergeRevokesViewSequenceAndRoutinePrivileges(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL+`
CREATE VIEW events_v AS SELECT id, user_email FROM events;
CREATE SEQUENCE counter;
CREATE FUNCTION leak() RETURNS text LANGUAGE sql AS $$ SELECT 'x' $$;
REVOKE EXECUTE ON FUNCTION leak() FROM PUBLIC;
`)
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role)
	f.exec(`GRANT SELECT ON events_v TO ` + f.role)
	f.exec(`GRANT SELECT, UPDATE, USAGE ON SEQUENCE counter TO ` + f.role)
	f.exec(`GRANT EXECUTE ON FUNCTION leak() TO ` + f.role)
	f.allowed(`SELECT user_email FROM events_v`, "hand-granted view")
	f.allowed(`SELECT nextval('counter')`, "hand-granted sequence")
	f.allowed(`SELECT leak()`, "hand-granted function")

	f.converge(eventsTable(Column{Name: "id"}))
	f.denied(`SELECT user_email FROM events_v`, "view")
	f.denied(`SELECT nextval('counter')`, "sequence")
	f.denied(`SELECT last_value FROM counter`, "sequence read")
	f.denied(`SELECT leak()`, "function")
	f.allowed(`SELECT id FROM events`, "the declared column")
}

// ALTER DEFAULT PRIVILEGES entries name the role for objects that do not exist
// yet. A table created after convergence must not be readable.
func TestConvergeRevokesDefaultPrivileges(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role)
	f.exec(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ` + f.role)
	f.exec(`ALTER DEFAULT PRIVILEGES GRANT SELECT, UPDATE ON SEQUENCES TO ` + f.role)
	f.exec(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO ` + f.role)
	f.exec(`CREATE TABLE before_converge (id TEXT)`)
	f.allowed(`SELECT id FROM before_converge`, "default privilege, table created before")

	f.converge(eventsTable(Column{Name: "id"}))

	f.exec(`CREATE TABLE after_converge (id TEXT)`)
	f.exec(`CREATE SEQUENCE after_seq`)
	f.denied(`SELECT id FROM before_converge`, "existing table")
	f.denied(`SELECT id FROM after_converge`, "table created after convergence, default privilege")
	f.denied(`SELECT last_value FROM after_seq`, "sequence created after convergence, default privilege")
	var n int
	if err := f.db.QueryRowContext(context.Background(), `
SELECT count(*) FROM pg_default_acl d, aclexplode(d.defaclacl) x
 WHERE x.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)`, f.role).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d default-privilege entries still name the role", n)
	}
}

// A privilege the migrating role cannot revoke (recorded under another
// grantor) fails the run rather than standing behind a converged-looking one,
// and the role does not keep silently-readable data.
func TestConvergeFailsClosedOnAPrivilegeItCannotRevoke(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	mid := f.role + "_mid"
	f.exec(`CREATE ROLE ` + mid + ` NOLOGIN`)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP OWNED BY `+mid)
		_, _ = f.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+mid)
	})
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + mid + `, ` + f.role)
	f.exec(`GRANT SELECT (user_email) ON events TO ` + mid + ` WITH GRANT OPTION`)
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+mid); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `GRANT SELECT (user_email) ON events TO `+f.role); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}

	script, err := RenderFor(f.role, eventsTable(Column{Name: "id"}))
	if err != nil {
		t.Fatal(err)
	}
	err = Apply(ctx, f.db, script)
	if err == nil {
		t.Fatal("the run converged while a privilege granted by another role still stands")
	}
	if !strings.Contains(err.Error(), "public.events.user_email") {
		t.Errorf("the failure does not name the object: %v", err)
	}
}

// Granting nothing further than what is declared: the script is idempotent and
// a clean role is left holding exactly the declared column privileges.
func TestConvergeIsIdempotentAndExact(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	tables := eventsTable(Column{Name: "id"}, Column{Name: "model"})
	f.converge(tables)
	f.converge(tables)
	var cols string
	if err := f.db.QueryRowContext(context.Background(), `
SELECT string_agg(table_name || '.' || column_name || ':' || privilege_type, ',' ORDER BY table_name, column_name)
  FROM information_schema.column_privileges WHERE grantee = $1`, f.role).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if want := "events.id:SELECT,events.model:SELECT"; cols != want {
		t.Errorf("column privileges = %q, want %q", cols, want)
	}
	var tbl int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM information_schema.table_privileges WHERE grantee = $1`, f.role).Scan(&tbl); err != nil {
		t.Fatal(err)
	}
	if tbl != 0 {
		t.Errorf("%d table-level privileges, want 0", tbl)
	}
}

// The migrator is the application's owner role, not a superuser, and the
// schema holds objects it does not own (an extension's functions). Converging
// must neither fail on them nor miss what it does own.
func TestConvergeAsANonSuperuserMigratorWithForeignObjects(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	app := f.role + "_app"
	f.exec(`CREATE ROLE ` + app + ` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE`)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP OWNED BY `+app)
		_, _ = f.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+app)
	})
	f.exec(`CREATE FUNCTION foreign_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`)
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + app)
	f.exec(`GRANT CREATE ON SCHEMA public TO ` + app)
	for _, tbl := range []string{"events", "other"} {
		f.exec(`ALTER TABLE ` + tbl + ` OWNER TO ` + app)
	}
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role)
	f.exec(`GRANT SELECT (user_email) ON events TO ` + f.role)

	script, err := RenderFor(f.role, eventsTable(Column{Name: "id"}))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+app); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, conn, script); err != nil {
		t.Fatalf("a non-superuser migrator could not converge: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	f.denied(`SELECT user_email FROM events`, "column revoked by the owner role")
	f.allowed(`SELECT id FROM events`, "declared column")
}

// Schema privileges beyond USAGE (CREATE would let the role make objects) are
// revoked; USAGE is the only one the role keeps.
func TestConvergeLeavesTheRoleOnlySchemaUsage(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.exec(`GRANT CREATE, USAGE ON SCHEMA public TO ` + f.role)
	f.converge(eventsTable(Column{Name: "id"}))
	var privs string
	if err := f.db.QueryRowContext(context.Background(), `
SELECT coalesce(string_agg(x.privilege_type, ',' ORDER BY x.privilege_type), '')
  FROM pg_namespace n, aclexplode(n.nspacl) x
 WHERE n.nspname = 'public' AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1)`, f.role).Scan(&privs); err != nil {
		t.Fatal(err)
	}
	if privs != "USAGE" {
		t.Errorf("schema privileges held by the role = %q, want USAGE only", privs)
	}
}

// A default privilege recorded for ANOTHER creating role cannot be revoked by a
// migrator that is not a member of it. The run fails and says so, rather than
// leaving every table that role creates readable.
func TestConvergeFailsClosedOnADefaultPrivilegeItCannotRevoke(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	creator, migrator := f.role+"_cr", f.role+"_mg"
	for _, r := range []string{creator, migrator} {
		f.exec(`CREATE ROLE ` + r + ` NOLOGIN NOSUPERUSER NOCREATEROLE`)
		r := r
		t.Cleanup(func() {
			_, _ = f.db.ExecContext(context.Background(), `DROP OWNED BY `+r)
			_, _ = f.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+r)
		})
	}
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role + `, ` + migrator)
	for _, tbl := range []string{"events", "other"} {
		f.exec(`ALTER TABLE ` + tbl + ` OWNER TO ` + migrator)
	}
	f.exec(`ALTER DEFAULT PRIVILEGES FOR ROLE ` + creator + ` IN SCHEMA public GRANT SELECT ON TABLES TO ` + f.role)

	script, err := RenderFor(f.role, eventsTable(Column{Name: "id"}))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+migrator); err != nil {
		t.Fatal(err)
	}
	err = Apply(ctx, conn, script)
	if err == nil {
		t.Fatal("the run converged while another role's default privilege still names the role")
	}
	if !strings.Contains(err.Error(), creator) || !strings.Contains(err.Error(), "default privileges") {
		t.Errorf("the failure does not name the creating role: %v", err)
	}
}

// A schema privilege the migrating role cannot revoke (it neither owns the
// schema nor holds the privilege with grant option) fails the run.
func TestConvergeFailsClosedOnASchemaPrivilegeItCannotRevoke(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL+`CREATE SCHEMA other_s; CREATE TABLE other_s.t (id TEXT);`)
	ctx := context.Background()
	migrator := f.role + "_mg"
	f.exec(`CREATE ROLE ` + migrator + ` NOLOGIN NOSUPERUSER`)
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP OWNED BY `+migrator)
		_, _ = f.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+migrator)
	})
	f.exec(`GRANT USAGE ON SCHEMA other_s TO ` + migrator)
	f.exec(`ALTER TABLE other_s.t OWNER TO ` + migrator)
	f.exec(`GRANT CREATE, USAGE ON SCHEMA other_s TO ` + f.role)

	script, err := RenderFor(f.role, []Table{{Schema: "other_s", Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+migrator); err != nil {
		t.Fatal(err)
	}
	err = Apply(ctx, conn, script)
	if err == nil {
		t.Fatal("the run converged while a schema CREATE the migrator cannot revoke still stands")
	}
	if !strings.Contains(err.Error(), "schema other_s (CREATE)") {
		t.Errorf("the failure does not name the schema privilege: %v", err)
	}
}

// forge_ro's script is not gated on login: a NOLOGIN role is converged too.
func TestReadOnlyScriptIsNotGatedOnLogin(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.exec(`ALTER ROLE ` + f.role + ` NOLOGIN`)
	f.converge(eventsTable(Column{Name: "id"}))
	f.allowed(`SELECT id FROM events`, "read-only role, declared column")
}

func TestRoleLoginReportsExistenceAndLogin(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	exists, login, err := RoleLogin(ctx, f.db, f.role)
	if err != nil || !exists || !login {
		t.Fatalf("login role: exists=%v login=%v err=%v", exists, login, err)
	}
	f.exec(`ALTER ROLE ` + f.role + ` NOLOGIN`)
	if exists, login, err = RoleLogin(ctx, f.db, f.role); err != nil || !exists || login {
		t.Fatalf("nologin role: exists=%v login=%v err=%v", exists, login, err)
	}
	if exists, login, err = RoleLogin(ctx, f.db, f.role+"_nope"); err != nil || exists || login {
		t.Fatalf("missing role: exists=%v login=%v err=%v", exists, login, err)
	}
}

func TestSecurityDefinerExposureNamesOnlyWhatTheRoleCanExecute(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL+`
CREATE FUNCTION open_definer() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;
CREATE FUNCTION invoker() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE FUNCTION closed_definer() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;
REVOKE EXECUTE ON FUNCTION closed_definer() FROM PUBLIC;
CREATE SCHEMA hidden;
CREATE FUNCTION hidden.definer_no_usage() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;
REVOKE USAGE ON SCHEMA hidden FROM PUBLIC;
`)
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + f.role)
	got, err := SecurityDefinerExposure(context.Background(), f.db, f.role)
	if err != nil {
		t.Fatal(err)
	}
	if want := "open_definer()"; len(got) != 1 || got[0] != want {
		t.Errorf("exposure = %v, want [%s]", got, want)
	}
	f.exec(`REVOKE EXECUTE ON FUNCTION open_definer() FROM PUBLIC`)
	if got, _ = SecurityDefinerExposure(context.Background(), f.db, f.role); len(got) != 0 {
		t.Errorf("exposure after revoking PUBLIC = %v, want none", got)
	}
}
