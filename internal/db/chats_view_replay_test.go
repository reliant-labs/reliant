package db

import (
	"database/sql"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// restoreLatestChatsView recreates chats_with_activity from the newest
// migration that defines it. A test that replays an OLDER migration's view
// leaves the database with that view, whose column set the current code no
// longer reads (CREATE OR REPLACE VIEW can only append columns, so a newer
// definition cannot be laid over an older one in place either). Dropping it
// first and creating the newest returns the schema the code expects.
func restoreLatestChatsView(t *testing.T, raw *sql.DB) {
	t.Helper()
	const head = "CREATE OR REPLACE VIEW chats_with_activity AS"
	files, err := fs.Glob(forgedb.MigrationsFS, "migrations/*.up.sql")
	require.NoError(t, err)
	sort.Strings(files)
	var stmt string
	for _, f := range files {
		body, err := forgedb.MigrationsFS.ReadFile(f)
		require.NoError(t, err)
		if i := strings.Index(string(body), head); i >= 0 {
			rest := string(body)[i:]
			end := strings.Index(rest, ") base;")
			require.GreaterOrEqual(t, end, 0, "%s: the view statement ends with ') base;'", f)
			stmt = rest[:end+len(") base;")]
		}
	}
	require.NotEmpty(t, stmt, "no migration defines chats_with_activity")
	_, err = raw.Exec("DROP VIEW IF EXISTS chats_with_activity")
	require.NoError(t, err)
	_, err = raw.Exec(stmt)
	require.NoError(t, err)
}
