package migratekit

import (
	"testing"
	"testing/fstest"
)

// A migration names the role when the role's name appears as a whole word,
// in any case, anywhere in the file: a GRANT, a quoted identifier, a DO body.
func TestNamesWord(t *testing.T) {
	for text, want := range map[string]bool{
		"GRANT SELECT (id) ON users TO forge_dash;":                      true,
		`grant select on users to "forge_dash";`:                         true,
		"DO $$ BEGIN EXECUTE 'GRANT SELECT ON t TO FORGE_DASH'; END $$;": true,
		"REVOKE SELECT (email) ON users FROM forge_dash":                 true,
		"CREATE TABLE forge_dashboards (id int);":                        false,
		"CREATE TABLE my_forge_dash (id int);":                           false,
		"CREATE TABLE users (id int);":                                   false,
	} {
		if got := namesWord(text, "forge_dash"); got != want {
			t.Errorf("namesWord(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestScanDashboardRefsListsTheMigrationsNamingTheRole(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/1_users.up.sql":  {Data: []byte("CREATE TABLE users (id int);")},
		"migrations/2_grant.up.sql":  {Data: []byte("GRANT SELECT (id) ON users TO forge_dash;")},
		"migrations/3_other.up.sql":  {Data: []byte("CREATE TABLE forge_dashboards (id int);")},
		"migrations/4_revoke.up.sql": {Data: []byte("REVOKE SELECT (id) ON users FROM forge_dash;")},
	}
	source, err := scanSource(fsys, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := scanDashboardRefs(fsys, "migrations", source, "forge_dash")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != (dashboardRef{2, "2_grant.up.sql"}) || refs[1] != (dashboardRef{4, "4_revoke.up.sql"}) {
		t.Errorf("refs = %+v, want 2_grant and 4_revoke", refs)
	}
}
