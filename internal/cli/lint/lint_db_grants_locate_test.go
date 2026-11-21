package lint

import "testing"

// TestDBGrantsLocate pins TL-52: a credential column gets a file:line whether
// a CREATE TABLE declares it or a one-line ALTER TABLE ADD COLUMN adds it.
func TestDBGrantsLocate(t *testing.T) {
	ms := migrationSources{
		{path: "/p/db/migrations/1_create.up.sql", text: "CREATE TABLE tasks (\n  id TEXT PRIMARY KEY,\n  api_key TEXT\n);\n"},
		{path: "/p/db/migrations/2_other.up.sql", text: "-- tasks get a token\nALTER TABLE accounts ADD COLUMN access_token TEXT;\n"},
		{path: "/p/db/migrations/3_alter.up.sql", text: "-- tasks get a token\nALTER TABLE public.tasks ADD COLUMN IF NOT EXISTS access_token TEXT;\nALTER TABLE \"tasks\" ADD refresh_token TEXT;\n"},
		{path: "/p/db/migrations/4_multi.up.sql", text: "ALTER TABLE tasks\n  ADD COLUMN client_secret TEXT;\n"},
	}
	for _, tc := range []struct {
		column, file string
		line         int
	}{
		{"api_key", "db/migrations/1_create.up.sql", 3},
		{"access_token", "db/migrations/3_alter.up.sql", 2},
		{"refresh_token", "db/migrations/3_alter.up.sql", 3},
		{"client_secret", "db/migrations/4_multi.up.sql", 2},
	} {
		file, line := ms.locate("/p", "tasks", tc.column)
		if file != tc.file || line != tc.line {
			t.Errorf("locate(tasks.%s) = %s:%d, want %s:%d", tc.column, file, line, tc.file, tc.line)
		}
	}
}
