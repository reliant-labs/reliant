package dbgrants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive the dashboard role's two checks against real Postgres. Like
// the convergence tests they boot a server, so they skip under -short. Roles
// are cluster-wide and the server is shared, so each test names its own.

func uniqueRole(prefix string) string {
	return fmt.Sprintf("%s%d_%d", prefix, os.Getpid(), time.Now().UnixNano()%1_000_000_000)
}

func dropRoleAtCleanup(t *testing.T, f *pgFixture, role string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP OWNED BY `+role)
		_, _ = f.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+role)
	})
}

// EnsureRole creates a missing role NOLOGIN, leaves an existing one exactly as
// it is (its LOGIN belongs to CNPG), and is idempotent.
func TestEnsureRoleCreatesAMissingRoleNologinAndLeavesAnExistingOneAlone(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	role := uniqueRole("forge_er")
	dropRoleAtCleanup(t, f, role)

	created, err := EnsureRole(ctx, f.db, role)
	if err != nil || !created {
		t.Fatalf("first EnsureRole: created=%v err=%v", created, err)
	}
	var login, inherit bool
	if err := f.db.QueryRowContext(ctx, `SELECT rolcanlogin, rolinherit FROM pg_roles WHERE rolname = $1`, role).Scan(&login, &inherit); err != nil {
		t.Fatal(err)
	}
	if login || inherit {
		t.Errorf("created role: login=%v inherit=%v, want NOLOGIN NOINHERIT", login, inherit)
	}

	// The deploy turns it on; a later run must not turn it back off.
	f.exec(`ALTER ROLE ` + role + ` LOGIN CONNECTION LIMIT 4`)
	if created, err = EnsureRole(ctx, f.db, role); err != nil || created {
		t.Fatalf("second EnsureRole: created=%v err=%v", created, err)
	}
	var limit int
	if err := f.db.QueryRowContext(ctx, `SELECT rolcanlogin, rolconnlimit FROM pg_roles WHERE rolname = $1`, role).Scan(&login, &limit); err != nil {
		t.Fatal(err)
	}
	if !login || limit != 4 {
		t.Errorf("EnsureRole altered an existing role: login=%v connlimit=%d", login, limit)
	}
}

// Migrators of different databases on one server race to create the role:
// every one of them succeeds, and exactly one reports creating it.
func TestEnsureRoleIsSafeUnderConcurrentCreation(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	role := uniqueRole("forge_ec")
	dropRoleAtCleanup(t, f, role)

	const n = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		creates int
		errs    []error
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := EnsureRole(context.Background(), f.db, role)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if created {
				creates++
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent EnsureRole failed: %v", errs)
	}
	if creates != 1 {
		t.Errorf("%d callers report creating the role, want exactly 1", creates)
	}
}

// A connecting role without CREATEROLE (a ManagedDatabase's application role)
// gets ErrRoleNotCreatable, not a generic failure, so the migrator can decide.
func TestEnsureRoleReportsAConnectingRoleThatMayNotCreateRoles(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	ctx := context.Background()
	role := uniqueRole("forge_en")
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// f.role is NOSUPERUSER NOCREATEROLE.
	if _, err := conn.ExecContext(ctx, `SET ROLE `+f.role); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureRole(ctx, conn, role)
	if created || !errors.Is(err, ErrRoleNotCreatable) {
		t.Fatalf("EnsureRole as a role without CREATEROLE: created=%v err=%v, want ErrRoleNotCreatable", created, err)
	}
}

// What a read-only role may hold is not listed; everything else is, by name.
func TestDashboardPrivilegeViolationsListsOnlyWhatIsNotReading(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL+`
CREATE SEQUENCE events_seq;
CREATE VIEW events_v AS SELECT id, model FROM events;
CREATE FUNCTION helper() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;
`)
	ctx := context.Background()
	role := f.role
	violations := func() []string {
		t.Helper()
		got, err := DashboardPrivilegeViolations(ctx, f.db, role)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got))
		for i, v := range got {
			out[i] = v.String()
		}
		return out
	}

	// The whole read-only vocabulary: none of it is a violation.
	f.exec(`GRANT USAGE ON SCHEMA public TO ` + role)
	f.exec(`GRANT SELECT (id, model) ON events TO ` + role)
	f.exec(`GRANT SELECT ON other TO ` + role)
	f.exec(`GRANT SELECT ON events_v TO ` + role)
	f.exec(`GRANT SELECT ON SEQUENCE events_seq TO ` + role)
	f.exec(`GRANT CONNECT ON DATABASE "` + currentDB(t, f) + `" TO ` + role)
	if got := violations(); len(got) != 0 {
		t.Fatalf("read privileges reported as violations: %v", got)
	}

	cases := []struct{ grant, revoke, want string }{
		{`GRANT INSERT ON events TO %s`, `REVOKE INSERT ON events FROM %s`, "INSERT on table events (fix: REVOKE INSERT ON TABLE events FROM"},
		{`GRANT UPDATE (note) ON events TO %s`, `REVOKE UPDATE (note) ON events FROM %s`, "UPDATE on table events"},
		{`GRANT SELECT ON other TO %s WITH GRANT OPTION`, `REVOKE GRANT OPTION FOR SELECT ON other FROM %s`, "SELECT WITH GRANT OPTION on table other (fix: REVOKE GRANT OPTION FOR SELECT"},
		{`GRANT USAGE ON SEQUENCE events_seq TO %s`, `REVOKE USAGE ON SEQUENCE events_seq FROM %s`, "USAGE on sequence events_seq"},
		{`GRANT CREATE ON SCHEMA public TO %s`, `REVOKE CREATE ON SCHEMA public FROM %s`, "CREATE on schema public"},
		{`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s`, `ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT ON TABLES FROM %s`, " in schema public on new tables"},
		{`GRANT pg_read_all_data TO %s`, `REVOKE pg_read_all_data FROM %s`, "membership in role pg_read_all_data"},
		{`ALTER ROLE %s CREATEDB`, `ALTER ROLE %s NOCREATEDB`, "role attribute CREATEDB"},
		// Effective privileges: what PUBLIC holds, the role holds.
		{`GRANT DELETE ON events TO PUBLIC -- %s`, `REVOKE DELETE ON events FROM PUBLIC -- %s`, "DELETE on table events, held through PUBLIC (fix: REVOKE DELETE ON TABLE events FROM PUBLIC)"},
		{`GRANT SELECT ON events TO PUBLIC -- %s`, `REVOKE SELECT ON events FROM PUBLIC -- %s`, "SELECT on table events is granted to PUBLIC, so " + role + " reads all of it"},
		{`GRANT SELECT (note) ON events TO PUBLIC -- %s`, `REVOKE SELECT (note) ON events FROM PUBLIC -- %s`, "SELECT on some columns of table events is granted to PUBLIC"},
		{`GRANT CREATE ON SCHEMA public TO PUBLIC -- %s`, `REVOKE CREATE ON SCHEMA public FROM PUBLIC -- %s`, "CREATE on schema public, held through PUBLIC (fix: REVOKE CREATE ON SCHEMA public FROM PUBLIC, run as the owner of SCHEMA public ("},
		// Grant options on read privileges: the role could hand them on.
		{`GRANT SELECT ON SEQUENCE events_seq TO %s WITH GRANT OPTION`, `REVOKE GRANT OPTION FOR SELECT ON SEQUENCE events_seq FROM %s`, "SELECT WITH GRANT OPTION on sequence events_seq"},
		{`GRANT USAGE ON SCHEMA public TO %s WITH GRANT OPTION`, `REVOKE GRANT OPTION FOR USAGE ON SCHEMA public FROM %s`, "USAGE WITH GRANT OPTION on schema public"},
		// PUBLIC's default privileges beyond reading; SELECT is allowed (below).
		{`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT INSERT ON TABLES TO PUBLIC -- %s`, `ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE INSERT ON TABLES FROM PUBLIC -- %s`, "on new tables grant INSERT to PUBLIC (fix: ALTER DEFAULT PRIVILEGES FOR ROLE"},
		// Ownership carries every privilege with no ACL entry.
		{`ALTER TABLE other OWNER TO %s`, `ALTER TABLE other OWNER TO CURRENT_USER -- %s`, "owns table other"},
		{`ALTER SEQUENCE events_seq OWNER TO %s`, `ALTER SEQUENCE events_seq OWNER TO CURRENT_USER -- %s`, "owns sequence events_seq"},
		{`ALTER FUNCTION helper() OWNER TO %s`, `ALTER FUNCTION helper() OWNER TO CURRENT_USER -- %s`, "owns function helper()"},
	}
	if serverVersion(t, f) >= 150000 {
		cases = append(cases, struct{ grant, revoke, want string }{
			`GRANT ALTER SYSTEM ON PARAMETER archive_command TO %s`, `REVOKE ALTER SYSTEM ON PARAMETER archive_command FROM %s`,
			"ALTER SYSTEM on parameter archive_command (fix: REVOKE ALTER SYSTEM ON PARAMETER archive_command FROM",
		})
	}
	for _, c := range cases {
		f.exec(fmt.Sprintf(c.grant, role))
		got := violations()
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("after %q: violations = %v, want one naming %q", fmt.Sprintf(c.grant, role), got, c.want)
		}
		f.exec(fmt.Sprintf(c.revoke, role))
		if got := violations(); len(got) != 0 {
			t.Errorf("after %q: violations = %v, want none", fmt.Sprintf(c.revoke, role), got)
		}
	}

	// PUBLIC's default SELECT on new tables, and Postgres's own defaults
	// re-stated, are reading.
	f.exec(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO PUBLIC`)
	f.exec(`ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`)
	f.exec(`ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO PUBLIC`)
	if got := violations(); len(got) != 0 {
		t.Errorf("PUBLIC's reading defaults: violations = %v, want none", got)
	}
	f.exec(`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT ON TABLES FROM PUBLIC`)

	// EXECUTE is PUBLIC's default on every function and is not listed; a
	// SECURITY DEFINER function is SecurityDefinerExposure's warning.
	f.exec(`GRANT EXECUTE ON FUNCTION helper() TO ` + role)
	if got := violations(); len(got) != 0 {
		t.Errorf("a direct EXECUTE grant: violations = %v, want none", got)
	}

	if got, err := DashboardPrivilegeViolations(ctx, f.db, role+"_missing"); err != nil || len(got) != 0 {
		t.Errorf("a missing role: violations=%v err=%v, want none", got, err)
	}
}

func serverVersion(t *testing.T, f *pgFixture) int {
	t.Helper()
	var v int
	if err := f.db.QueryRowContext(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func currentDB(t *testing.T, f *pgFixture) string {
	t.Helper()
	var name string
	if err := f.db.QueryRowContext(context.Background(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}
