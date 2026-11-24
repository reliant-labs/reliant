package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestDBQueryLocalReadsAsTheReadOnlyRole pins NS FG-24: a local env reached
// through the workload's DATABASE_URL reads as forge_ro once its grants are
// applied, so a forge:secret column (granted to nobody) is refused in dev as
// it is everywhere else; --app-role reads as the app's own role on purpose.
func TestDBQueryLocalReadsAsTheReadOnlyRole(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{dbgrants.RoleName}.Sanitize()
	// The role is server-wide and other tests may create it too.
	for _, stmt := range []string{
		`DO $$ BEGIN CREATE ROLE ` + role + ` NOLOGIN; EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`,
		`CREATE TABLE tasks (id int PRIMARY KEY, api_key text)`,
		`INSERT INTO tasks VALUES (1, 'sample_api_key_1')`,
		`GRANT USAGE ON SCHEMA public TO ` + role,
		`GRANT SELECT (id) ON tasks TO ` + role,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = conn.Close(ctx)

	withKCLFixture(t, dbQueryFixture(RuntimeHost, dsn))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	if out, err := runDBQueryCmd(t, "dev", "select id from tasks", "--format", "csv"); err != nil || out != "id\n1\n" {
		t.Fatalf("a granted column: %v %q", err, out)
	}
	if out, err := runDBQueryCmd(t, "dev", "select current_user", "--format", "csv"); err != nil || !strings.Contains(out, dbgrants.RoleName) {
		t.Fatalf("current_user: %v %q, want %s", err, out, dbgrants.RoleName)
	}
	if out, err := runDBQueryCmd(t, "dev", "select api_key from tasks"); err == nil {
		t.Fatalf("a secret column was readable in dev:\n%s", out)
	}
	if out, err := runDBQueryCmd(t, "dev", "select api_key from tasks", "--format", "csv", "--app-role"); err != nil || !strings.Contains(out, "sample_api_key_1") {
		t.Fatalf("--app-role: %v %q", err, out)
	}
}

// TestDBQueryLocalRefusesAnAppRoleThatCannotAssumeTheReadOnlyRole: once
// forge_ro's grants are applied, an app role that may not SET ROLE to it is
// refused rather than read as itself, which would expose forge:secret columns
// while the command promises the restricted role. --app-role still reads.
func TestDBQueryLocalRefusesAnAppRoleThatCannotAssumeTheReadOnlyRole(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{dbgrants.RoleName}.Sanitize()
	// Roles are server-wide: a name of its own, so a parallel test's role is
	// never this one.
	app := fmt.Sprintf("forge_app_%d", time.Now().UnixNano())
	appID := pgx.Identifier{app}.Sanitize()
	for _, stmt := range []string{
		`DO $$ BEGIN CREATE ROLE ` + role + ` NOLOGIN; EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`,
		`CREATE ROLE ` + appID + ` LOGIN PASSWORD 'app'`,
		`CREATE TABLE tasks (id int PRIMARY KEY, api_key text)`,
		`INSERT INTO tasks VALUES (1, 'sample_api_key_1')`,
		`GRANT USAGE ON SCHEMA public TO ` + role + `, ` + appID,
		`GRANT SELECT (id) ON tasks TO ` + role,
		`GRANT SELECT ON tasks TO ` + appID,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), `DROP TABLE IF EXISTS tasks`)
		_, _ = c.Exec(context.Background(), `DROP ROLE IF EXISTS `+appID)
	})
	_ = conn.Close(ctx)

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(app, "app")
	withKCLFixture(t, dbQueryFixture(RuntimeHost, u.String()))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := runDBQueryCmd(t, "dev", "select api_key from tasks", "--format", "csv")
	if err == nil {
		t.Fatalf("an app role that cannot assume %s read as itself:\n%s", dbgrants.RoleName, out)
	}
	if !strings.Contains(err.Error(), "--app-role") {
		t.Errorf("the refusal should name --app-role: %v", err)
	}
	if out, err := runDBQueryCmd(t, "dev", "select api_key from tasks", "--format", "csv", "--app-role"); err != nil || !strings.Contains(out, "sample_api_key_1") {
		t.Fatalf("--app-role: %v %q", err, out)
	}
}
