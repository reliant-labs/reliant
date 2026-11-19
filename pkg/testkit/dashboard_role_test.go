package testkit

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// A test database applies a project's migrations under the migrator's
// dashboard-role rules (pkg/migratekit, dashboard.go): a migration granting
// the role works, and one granting it more than reads fails, exactly as in
// production. Privileges are per database, so these run in parallel with any
// other test on the shared server.
func TestApplyUpMigrationsKeepsTheDashboardRoleRules(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	t.Parallel()
	const users = `CREATE TABLE users (id BIGINT PRIMARY KEY, email TEXT, cost BIGINT);`
	for name, tc := range map[string]struct {
		grant   string
		wantErr string
	}{
		"a column grant applies":   {grant: `GRANT SELECT (id, cost) ON users TO ` + dbgrants.DashboardRoleName + `;`},
		"a write grant is refused": {grant: `GRANT UPDATE ON users TO ` + dbgrants.DashboardRoleName + `;`, wantErr: "UPDATE on table users"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, cleanup, err := pgtest.New()
			if err != nil {
				t.Fatalf("test postgres: %v", err)
			}
			t.Cleanup(cleanup)
			err = applyUpMigrations(db, fstest.MapFS{
				"migrations/1_users.up.sql":     {Data: []byte(users)},
				"migrations/2_dashboard.up.sql": {Data: []byte(tc.grant)},
			})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("applyUpMigrations: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("applyUpMigrations = %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}
