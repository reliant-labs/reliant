// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// #3483 merged 20261124202455_tool_call_usage below #3482's 20261124204326,
// which merged first, so a database at 20261124204326 never ran it. It was
// replaced by 20261124222627_tool_call_usage (forge:replaces, re-runnable).
// These tests boot the current binary's Migrate over both databases that
// history left behind: one that skipped the old version, and one created from
// main while the old file was there, which already holds the table.

const (
	toolUsageSkippedAt  = 20261124204326 // #3482's head: 202455 was never applied
	toolUsageOldFile    = "20261124202455_tool_call_usage.up.sql"
	toolUsageNewFile    = "20261124222627_tool_call_usage.up.sql"
	toolUsageAppliedRow = "replaced-migration-row"
)

// oldToolUsageSQL is the file as #3483 merged it: the replacement's SQL without
// its re-runnable guards.
func oldToolUsageSQL(t *testing.T) string {
	t.Helper()
	body, err := fs.ReadFile(forgedb.Source(), path.Join(forgedb.SourceDir, toolUsageNewFile))
	require.NoError(t, err)
	old := strings.NewReplacer(
		"CREATE TABLE IF NOT EXISTS", "CREATE TABLE",
		"CREATE INDEX IF NOT EXISTS", "CREATE INDEX",
		"CREATE OR REPLACE TRIGGER", "CREATE TRIGGER",
	).Replace(string(body))
	require.NotEqual(t, string(body), old, "%s is no longer written re-runnably", toolUsageNewFile)
	return old
}

func requireToolUsageSchema(t *testing.T, dsn string) {
	t.Helper()
	db := openTestDSN(t, dsn)
	var indexes, triggers int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE tablename = 'tool_call_usage' AND indexname IN
		('tool_call_usage_created_at_idx', 'tool_call_usage_user_created_at_idx', 'tool_call_usage_chat_id_idx')`).Scan(&indexes))
	require.Equal(t, 3, indexes, "tool_call_usage has its three indexes")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_trigger
		WHERE tgrelid = 'tool_call_usage'::regclass AND tgname = 'chat_write_admission' AND NOT tgisinternal`).Scan(&triggers))
	require.Equal(t, 1, triggers, "tool_call_usage has its chat_write_admission trigger")
}

func TestMigrateFromStateThatSkippedToolCallUsageCreatesIt(t *testing.T) {
	t.Parallel()
	dsn := newEmptyTestDB(t)
	raw := openTestDSN(t, dsn)
	_, err := migratekit.Apply(context.Background(), raw, embeddedUpTo(t, toolUsageSkippedAt, nil), forgedb.SourceDir)
	require.NoError(t, err)
	require.Equal(t, uint(toolUsageSkippedAt), migrationState(t, raw).Version)

	_, err = migrate(dsn)
	require.NoError(t, err, "the current binary migrates a database that skipped %s", toolUsageOldFile)
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
	requireToolUsageSchema(t, dsn)
}

func TestMigrateFromStateThatRanOldToolCallUsageKeepsItsRows(t *testing.T) {
	t.Parallel()
	dsn := newEmptyTestDB(t)
	raw := openTestDSN(t, dsn)
	old := embeddedUpTo(t, toolUsageSkippedAt, map[string]string{toolUsageOldFile: oldToolUsageSQL(t)})
	_, err := migratekit.Apply(context.Background(), raw, old, forgedb.SourceDir)
	require.NoError(t, err)
	require.Equal(t, uint(toolUsageSkippedAt), migrationState(t, raw).Version)
	requireToolUsageSchema(t, dsn)
	_, err = raw.Exec(`INSERT INTO tool_call_usage (id, attempt, user_id, chat_id, tool_name, tool_kind, location, outcome)
		VALUES ($1, 1, 'u', 'c', 'bash', 'builtin', 'local', 'success')`, toolUsageAppliedRow)
	require.NoError(t, err)

	_, err = migrate(dsn)
	require.NoError(t, err, "the current binary migrates a database that ran %s", toolUsageOldFile)
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
	requireToolUsageSchema(t, dsn)
	var n int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM tool_call_usage WHERE id = $1`, toolUsageAppliedRow).Scan(&n))
	require.Equal(t, 1, n, "the replacement keeps the rows the old table holds")

	_, err = migrate(dsn)
	require.NoError(t, err, "a second boot is a no-op")
}
