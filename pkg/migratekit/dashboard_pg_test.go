package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The dashboard role exists before any migration runs, and holds read
// privileges only after (dashboard.go). The project grants it with ordinary
// migrations, so these tests write those migrations and then ACT AS the role.
//
// Like the read-only tests these swap a package variable (dashboardRole) and
// use a role of their own, so none calls t.Parallel.

// useDashboardRole points the migrator at a dashboard role no other test
// process shares, and drops it at the end. The role is NOT created: the
// migrator does that.
func useDashboardRole(t *testing.T, dsn string) string {
	t.Helper()
	name := fmt.Sprintf("forge_dash_t%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	prev := dashboardRole
	dashboardRole = name
	admin := openRecordDB(t, dsn)
	t.Cleanup(func() {
		dashboardRole = prev
		c := context.Background()
		// A database the role was made to own is not dropped by DROP OWNED.
		_, _ = admin.ExecContext(c, `REASSIGN OWNED BY `+name+` TO CURRENT_USER`)
		_, _ = admin.ExecContext(c, `DROP OWNED BY `+name)
		_, _ = admin.ExecContext(c, `DROP ROLE IF EXISTS `+name)
	})
	return name
}

// enableLogin does what the deploy does when dashboards are on: the role can
// log in with a password. It returns the password.
func enableLogin(t *testing.T, dsn, role string) string {
	t.Helper()
	const pw = "dash-login-pw"
	if _, err := openRecordDB(t, dsn).Exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD '%s'`, role, pw)); err != nil {
		t.Fatal(err)
	}
	return pw
}

// roleDSN is dsn with the user and password swapped for the role's, so a test
// can actually try to log in as it.
func roleDSN(t *testing.T, dsn, role, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, password)
	return u.String()
}

// loginAs reports whether a connection as role succeeds and runs q.
func loginAs(t *testing.T, dsn, role, password, q string) error {
	t.Helper()
	db, err := sql.Open("pgx/v5", sqlDSN(roleDSN(t, dsn, role, password)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	return rows.Err()
}

func privilegeRowsOf(t *testing.T, dsn, role string) int {
	t.Helper()
	var n int
	err := openRecordDB(t, dsn).QueryRow(`
SELECT (SELECT count(*) FROM information_schema.table_privileges  WHERE grantee = $1)
     + (SELECT count(*) FROM information_schema.column_privileges WHERE grantee = $1)`, role).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func grantDashboard(role string) string {
	return fmt.Sprintf("GRANT SELECT (id, email) ON users TO %s;", role)
}

// TestDashboardRoleExistsBeforeTheMigrationThatGrantsIt: a fresh database has
// no dashboard role; the run creates it NOLOGIN first, so the project's own
// GRANT migration applies, and the role then reads exactly what it was granted.
func TestDashboardRoleExistsBeforeTheMigrationThatGrantsIt(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	fsys := release(map[string]string{
		"1_users.up.sql":     usersSQL + notesSQL,
		"2_dashboard.up.sql": grantDashboard(dash),
	})

	res := mustUp(t, fsys, dsn)
	if res.Dashboard == nil || !res.Dashboard.Created || res.Dashboard.Missing || res.Dashboard.CanLogin {
		t.Fatalf("Dashboard = %+v, want created, NOLOGIN", res.Dashboard)
	}
	var login bool
	if err := openRecordDB(t, dsn).QueryRow(`SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, dash).Scan(&login); err != nil {
		t.Fatal(err)
	}
	if login {
		t.Error("the migrator created a role that can log in: that is the deploy's decision")
	}
	seed(t, dsn)
	pw := enableLogin(t, dsn, dash)
	if err := loginAs(t, dsn, dash, pw, `SELECT id, email FROM users`); err != nil {
		t.Errorf("the granted columns: %v", err)
	}
	wantDenied(t, loginAs(t, dsn, dash, pw, `SELECT api_token FROM users`), "a column no migration granted")
	wantDenied(t, loginAs(t, dsn, dash, pw, `SELECT * FROM users`), "select * over an ungranted column")
	wantDenied(t, loginAs(t, dsn, dash, pw, `UPDATE users SET email = 'x'`), "a write")
	wantDenied(t, loginAs(t, dsn, dash, pw, `CREATE TABLE public.dash_owned (i int)`), "CREATE in public")

	// The next run finds it and leaves it alone.
	if res := mustUp(t, fsys, dsn); res.Dashboard == nil || res.Dashboard.Created {
		t.Errorf("second run: Dashboard = %+v, want found, not created", res.Dashboard)
	}
}

// TestDashboardsOffKeepsTheGrantsButNobodyCanUseThem is the disable path
// against what CNPG does to the role on each side of the switch: on = LOGIN
// with a password; off = NOLOGIN with no password. The migrator never touches
// either, and never revokes: the grants are the project's migrations.
func TestDashboardsOffKeepsTheGrantsButNobodyCanUseThem(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	admin := openRecordDB(t, dsn)
	exec := func(q string) {
		t.Helper()
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	fsys := release(map[string]string{"1_users.up.sql": usersSQL + notesSQL, "2_dashboard.up.sql": grantDashboard(dash)})
	mustUp(t, fsys, dsn)
	seed(t, dsn)

	// ON: CNPG adopts the role the migrator created and gives it a login.
	const pw = "s3cret-on"
	exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD '%s'`, dash, pw))
	if res := mustUp(t, fsys, dsn); res.Dashboard == nil || !res.Dashboard.CanLogin {
		t.Fatalf("enabled: Dashboard = %+v, want CanLogin", res.Dashboard)
	}
	if err := loginAs(t, dsn, dash, pw, `SELECT id, email FROM users`); err != nil {
		t.Fatalf("the enabled role cannot read what a migration granted: %v", err)
	}

	// OFF: what CNPG converges the rendered role to.
	exec(fmt.Sprintf(`ALTER ROLE %s NOLOGIN PASSWORD NULL`, dash))
	res := mustUp(t, fsys, dsn)
	if res.Dashboard == nil || res.Dashboard.CanLogin {
		t.Fatalf("disabled: Dashboard = %+v, want not CanLogin", res.Dashboard)
	}
	if err := loginAs(t, dsn, dash, pw, `SELECT 1`); err == nil {
		t.Error("the disabled role logged in with its old password")
	}
	if err := loginAs(t, dsn, dash, "", `SELECT 1`); err == nil {
		t.Error("the disabled role logged in without a password")
	}
	if privilegeRowsOf(t, dsn, dash) == 0 {
		t.Error("the run revoked the project's grants: off must leave them in place, unusable")
	}

	// ON again: the same grants serve, with the new password only.
	const pw2 = "s3cret-back"
	exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD '%s'`, dash, pw2))
	mustUp(t, fsys, dsn)
	if err := loginAs(t, dsn, dash, pw2, `SELECT id, email FROM users`); err != nil {
		t.Errorf("the re-enabled role cannot read: %v", err)
	}
	if err := loginAs(t, dsn, dash, pw, `SELECT 1`); err == nil {
		t.Error("the password from before the role was disabled works again")
	}
}

// TestRevokingInALaterMigrationRemovesAccess: removing access is a migration
// too.
func TestRevokingInALaterMigrationRemovesAccess(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	files := map[string]string{"1_users.up.sql": usersSQL, "2_dashboard.up.sql": grantDashboard(dash)}
	mustUp(t, release(files), dsn)
	files["3_revoke.up.sql"] = fmt.Sprintf("REVOKE SELECT (email) ON users FROM %s;", dash)
	mustUp(t, release(files), dsn)
	pw := enableLogin(t, dsn, dash)
	if err := loginAs(t, dsn, dash, pw, `SELECT id FROM users`); err != nil {
		t.Errorf("a column still granted: %v", err)
	}
	wantDenied(t, loginAs(t, dsn, dash, pw, `SELECT email FROM users`), "a revoked column")
}

// migratorDSN creates a login role that owns nothing and may not create roles
// — a ManagedDatabase's application role — with what it needs to migrate the
// database, and returns a DSN that connects as it.
func migratorDSN(t *testing.T, dsn string) string {
	t.Helper()
	admin := openRecordDB(t, dsn)
	role := fmt.Sprintf("forge_mig_t%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	const pw = "migrator-pw"
	var db string
	if err := admin.QueryRow(`SELECT current_database()`).Scan(&db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEROLE NOCREATEDB`, role, pw),
		fmt.Sprintf(`GRANT ALL ON DATABASE "%s" TO %s`, db, role),
		fmt.Sprintf(`GRANT ALL ON SCHEMA public TO %s`, role),
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.ExecContext(c, `DROP OWNED BY `+role)
		_, _ = admin.ExecContext(c, `DROP ROLE IF EXISTS `+role)
	})
	return roleDSN(t, dsn, role, pw)
}

// TestAMigratorThatMayNotCreateTheRole: on a ManagedDatabase the migrating
// role has no CREATEROLE and CNPG may not have created the dashboard role yet.
// A run that grants nothing to it goes on; a run about to apply a migration
// that names it refuses BEFORE applying anything, so the schema is not left
// dirty, and succeeds once the role exists.
func TestAMigratorThatMayNotCreateTheRole(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	mig := migratorDSN(t, dsn)

	files := map[string]string{"1_users.up.sql": usersSQL}
	res := mustUp(t, release(files), mig)
	if res.Dashboard == nil || !res.Dashboard.Missing || res.Dashboard.Created {
		t.Fatalf("Dashboard = %+v, want missing and not created", res.Dashboard)
	}

	files["2_dashboard.up.sql"] = "-- panels read these\n" + grantDashboard(dash)
	mg, err := Open(Options{FS: release(files), Dir: "migrations", DSN: mig})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mg.Up()
	_ = mg.Close()
	var missing *DashboardRoleMissingError
	if !errors.As(err, &missing) || len(missing.Migrations) != 1 || missing.Migrations[0] != "2_dashboard.up.sql" {
		t.Fatalf("up = %v, want a DashboardRoleMissingError naming 2_dashboard.up.sql", err)
	}
	if !strings.Contains(err.Error(), "Nothing was applied") {
		t.Errorf("the refusal does not say nothing was applied: %v", err)
	}
	var version int
	var dirty bool
	if err := openRecordDB(t, dsn).QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 1 || dirty {
		t.Fatalf("schema at version %d dirty=%v after the refusal, want 1 and clean", version, dirty)
	}

	// CNPG reconciles the role; the retry applies the grant.
	if _, err := openRecordDB(t, dsn).Exec(`CREATE ROLE ` + dash + ` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if res := mustUp(t, release(files), mig); res.After.Version != 2 || res.Dashboard == nil || res.Dashboard.Missing {
		t.Fatalf("retry: version %d, Dashboard = %+v, want 2 and found", res.After.Version, res.Dashboard)
	}
}

// TestEveryWayToDoMoreThanReadFailsTheRun is the safety net, case by case,
// against a real login. Each case's migration hands the dashboard role a
// capability beyond reading — directly, through PUBLIC, through ownership, or
// through a parameter ACL. The case logs in as the role WITH A PASSWORD and
// uses the capability, proving it is real, and the run that applied the
// migration fails naming it, as does every run after until a migration takes
// it away.
func TestEveryWayToDoMoreThanReadFailsTheRun(t *testing.T) {
	cases := []struct {
		name    string
		grant   string // {role} is the role
		runtime string // run by the application after the migrations, before the role uses it
		use     string // run as the role, logged in
		want    string // in the run's error
		minimum int    // server_version_num the case needs
	}{
		{name: "a direct write grant", grant: `GRANT INSERT ON users TO {role};`,
			use: `INSERT INTO users (id, email) VALUES ('u9', 'w@example.test')`, want: "INSERT on table users"},
		{name: "a write grant to PUBLIC", grant: `GRANT INSERT, UPDATE, DELETE, TRUNCATE ON users TO PUBLIC;`,
			use: `UPDATE users SET api_token = 'x'`, want: "UPDATE on table users, held through PUBLIC (fix: REVOKE UPDATE ON TABLE users FROM PUBLIC)"},
		{name: "a whole-table SELECT to PUBLIC", grant: `GRANT SELECT ON users TO PUBLIC;`,
			use: `SELECT api_token FROM users`, want: "SELECT on table users is granted to PUBLIC"},
		{name: "CREATE on public held through PUBLIC (Postgres 14's default)", grant: `GRANT CREATE ON SCHEMA public TO PUBLIC;`,
			use:  `CREATE FUNCTION public.lower(varchar) RETURNS text LANGUAGE sql AS $$ SELECT 'pwned'::text $$`,
			want: "CREATE on schema public, held through PUBLIC (fix: REVOKE CREATE ON SCHEMA public FROM PUBLIC, run as the owner of SCHEMA public"},
		{name: "owning a table", grant: `ALTER TABLE users OWNER TO {role};`,
			use: `UPDATE users SET api_token = 'x'`, want: "owns table users"},
		{name: "owning a schema", grant: `CREATE SCHEMA dash_s; ALTER SCHEMA dash_s OWNER TO {role};`,
			use: `CREATE TABLE dash_s.mine (i int)`, want: "owns schema dash_s"},
		{name: "owning the database", grant: `DO $$ BEGIN EXECUTE format('ALTER DATABASE %I OWNER TO {role}', current_database()); END $$;`,
			use: `CREATE SCHEMA evil`, want: "owns database"},
		{name: "owning a large object", grant: `SELECT lo_create(424242); ALTER LARGE OBJECT 424242 OWNER TO {role};`,
			use: `SELECT lo_put(424242, 0, '\x01')`, want: "owns large object 424242"},
		{name: "ALTER SYSTEM on a parameter", grant: `GRANT ALTER SYSTEM ON PARAMETER archive_command TO {role};`,
			// RESET needs the same privilege and leaves the shared server as it was.
			use: `ALTER SYSTEM RESET archive_command`, want: "ALTER SYSTEM on parameter archive_command", minimum: 150000},
		{name: "default privileges handing PUBLIC writes on future tables",
			grant: `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT INSERT, UPDATE ON TABLES TO PUBLIC;`,
			// A partition the application creates between runs.
			runtime: `CREATE TABLE public.events_2026_11 (id int)`,
			use:     `INSERT INTO public.events_2026_11 VALUES (1)`, want: "on new tables grant INSERT to PUBLIC"},
		{name: "SELECT on a large object to PUBLIC",
			grant: `SELECT lo_from_bytea(424243, '\x01'::bytea); GRANT SELECT ON LARGE OBJECT 424243 TO PUBLIC;`,
			use:   `SELECT lo_get(424243)`, want: "SELECT on large object 424243 is granted to PUBLIC"},
		{name: "USAGE on a foreign-data wrapper",
			grant: `CREATE FOREIGN DATA WRAPPER dash_fdw; GRANT USAGE ON FOREIGN DATA WRAPPER dash_fdw TO {role};`,
			use:   `CREATE SERVER dash_own FOREIGN DATA WRAPPER dash_fdw`, want: "USAGE on foreign-data wrapper dash_fdw"},
		{name: "USAGE on a foreign server",
			grant: `CREATE FOREIGN DATA WRAPPER dash_fdw2; CREATE SERVER dash_srv FOREIGN DATA WRAPPER dash_fdw2; GRANT USAGE ON FOREIGN SERVER dash_srv TO {role};`,
			use:   `CREATE USER MAPPING FOR CURRENT_USER SERVER dash_srv`, want: "USAGE on foreign server dash_srv"},
		{name: "a grant option on sequence SELECT",
			grant: `CREATE SEQUENCE dash_seq; GRANT SELECT ON SEQUENCE dash_seq TO {role} WITH GRANT OPTION;`,
			use: handOn(`GRANT SELECT ON SEQUENCE dash_seq TO PUBLIC`, `has_sequence_privilege('public', 'dash_seq', 'SELECT')`,
				`REVOKE SELECT ON SEQUENCE dash_seq FROM PUBLIC`),
			want: "SELECT WITH GRANT OPTION on sequence dash_seq"},
		{name: "a grant option on schema USAGE",
			grant: `CREATE SCHEMA dash_g; GRANT USAGE ON SCHEMA dash_g TO {role} WITH GRANT OPTION;`,
			use: handOn(`GRANT USAGE ON SCHEMA dash_g TO PUBLIC`, `has_schema_privilege('public', 'dash_g', 'USAGE')`,
				`REVOKE USAGE ON SCHEMA dash_g FROM PUBLIC`),
			want: "USAGE WITH GRANT OPTION on schema dash_g"},
		{name: "a grant option on database CONNECT",
			grant: `DO $$ BEGIN EXECUTE format('GRANT CONNECT ON DATABASE %I TO {role} WITH GRANT OPTION', current_database()); END $$;`,
			// PUBLIC already holds CONNECT, so the proof is an ACL entry the role granted.
			use: handOn(`EXECUTE format('GRANT CONNECT ON DATABASE %I TO PUBLIC', current_database())`,
				`EXISTS (SELECT 1 FROM pg_database d, aclexplode(d.datacl) x WHERE d.datname = current_database() AND x.grantee = 0 AND x.grantor = current_user::regrole)`,
				`EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', current_database())`),
			want: "CONNECT WITH GRANT OPTION on database"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dsn := requirePG(t)
			if c.minimum > 0 {
				var v int
				if err := openRecordDB(t, dsn).QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&v); err != nil {
					t.Fatal(err)
				}
				if v < c.minimum {
					t.Skipf("needs server_version_num %d, have %d", c.minimum, v)
				}
			}
			dash := useDashboardRole(t, dsn)
			files := map[string]string{"1_users.up.sql": usersSQL + notesSQL}
			mustUp(t, release(files), dsn)
			seed(t, dsn)
			pw := enableLogin(t, dsn, dash)
			if err := loginAs(t, dsn, dash, pw, c.use); err == nil {
				t.Fatalf("the role could %q before any migration granted it: the case proves nothing", c.use)
			}

			files["2_grant.up.sql"] = strings.ReplaceAll(c.grant, "{role}", dash)
			up := func() error {
				mg, err := Open(Options{FS: release(files), Dir: "migrations", DSN: dsn})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = mg.Close() }()
				_, err = mg.Up()
				return err
			}
			for _, run := range []string{"the run that applied it", "the next run"} {
				err := up()
				var priv *DashboardPrivilegeError
				if !errors.As(err, &priv) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("%s: up = %v, want a DashboardPrivilegeError naming %q", run, err, c.want)
				}
			}
			if c.runtime != "" {
				if _, err := openRecordDB(t, dsn).Exec(c.runtime); err != nil {
					t.Fatal(err)
				}
			}
			// The capability the run refused is real.
			if err := loginAs(t, dsn, dash, pw, c.use); err != nil {
				t.Fatalf("logged in as the role, %q failed: %v; the check refused a capability that does not exist", c.use, err)
			}
		})
	}
}

// handOn is a statement the role runs to prove it can delegate a privilege:
// Postgres answers a GRANT the grantor holds no grant option for with a
// WARNING, not an error, so the block checks the grant took effect, raising
// otherwise, and takes it back so the role is not left a grantor.
func handOn(grant, took, undo string) string {
	return "DO $$ BEGIN " + grant + "; IF NOT (" + took + ") THEN RAISE EXCEPTION 'nothing was granted'; END IF; " + undo + "; END $$"
}

// TestRevokingAWriteGrantLetsTheRunFinish: the fix the error names works.
func TestRevokingAWriteGrantLetsTheRunFinish(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	files := map[string]string{
		"1_users.up.sql":     usersSQL,
		"2_dashboard.up.sql": grantDashboard(dash) + "\nGRANT CREATE ON SCHEMA public TO PUBLIC;",
	}
	up := func() (Result, error) {
		mg, err := Open(Options{FS: release(files), Dir: "migrations", DSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = mg.Close() }()
		return mg.Up()
	}
	if _, err := up(); err == nil {
		t.Fatal("a run after GRANT CREATE ON SCHEMA public TO PUBLIC succeeded")
	}
	files["3_revoke.up.sql"] = "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"
	if res, err := up(); err != nil || res.After.Version != 3 {
		t.Fatalf("after the revoking migration: version %d err %v, want 3 and no error", res.After.Version, err)
	}
	pw := enableLogin(t, dsn, dash)
	wantDenied(t, loginAs(t, dsn, dash, pw, `CREATE TABLE public.dash_owned (i int)`), "CREATE in public after the revoke")
}

// TestApplyEnsuresTheDashboardRoleToo: the boot path (AutoMigrate -> Apply)
// creates the role and checks it exactly as Up does.
func TestApplyEnsuresTheDashboardRoleToo(t *testing.T) {
	dsn := requirePG(t)
	dash := useDashboardRole(t, dsn)
	db := openRecordDB(t, dsn)
	res, err := Apply(context.Background(), db, release(map[string]string{
		"1_users.up.sql": usersSQL, "2_dashboard.up.sql": grantDashboard(dash),
	}), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dashboard == nil || !res.Dashboard.Created {
		t.Fatalf("Dashboard = %+v, want created", res.Dashboard)
	}
}

// TestSecurityDefinerFunctionsAreReported: a SECURITY DEFINER function the
// converged role may execute is a way around its column grants. The run still
// succeeds; it says so.
func TestSecurityDefinerFunctionsAreReported(t *testing.T) {
	dsn := requirePG(t)
	useRole(t)
	withRole(t, dsn)
	fsys := grantsRelease(map[string]string{
		"1_users.up.sql": usersSQL,
		"2_definer.up.sql": `CREATE FUNCTION get_token(uid TEXT) RETURNS TEXT LANGUAGE sql SECURITY DEFINER AS $$ SELECT api_token FROM users WHERE id = uid $$;
CREATE FUNCTION invoker_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
CREATE FUNCTION locked_definer() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$ SELECT 1 $$;
REVOKE EXECUTE ON FUNCTION locked_definer() FROM PUBLIC;`,
	}, mustRender(t, usersTable(true)))
	res := mustUp(t, fsys, dsn)
	if res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("the run did not apply the grants: %+v", res.Grants)
	}
	if got := strings.Join(res.Grants.SecurityDefiner, ","); got != "get_token(text)" {
		t.Fatalf("SecurityDefiner = %q, want only get_token(text) (not the invoker's function, not the one PUBLIC cannot execute)", got)
	}
	if _, err := openRecordDB(t, dsn).Exec(`INSERT INTO users (id, email, api_token) VALUES ('u1', 'a@example.test', 'tok-SECRET-1')`); err != nil {
		t.Fatal(err)
	}
	// The warning is accurate: the role really does read the secret through it.
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		wantDenied(t, readable(ctx, c, `SELECT api_token FROM users`), "the column directly")
		if err := readable(ctx, c, `SELECT get_token('u1')`); err != nil {
			t.Errorf("the exposure the warning describes does not exist: %v", err)
		}
	})
}
