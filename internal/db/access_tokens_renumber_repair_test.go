// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The renumber window
// -------------------
// #293 added the access-tokens migration as 20260923000000_access_tokens_retire_pats.sql,
// a version 20260923000000_temporal_payload_blobs.sql already owned. #294
// renumbered it to 20260925000000 an hour later. A database that ran main in
// between recorded goose version 20260923000000 as applied — but the SQL that
// ran under that version was the ACCESS-TOKENS one. Such a database therefore
// has access_tokens and no temporal_payload_blobs, and 20260925000000 is still
// pending.
//
// On the next startup goose.Up(WithAllowMissing) reaches 20260925000000 and
// fails on `CREATE TABLE access_tokens` because the table is already there.
// api-server exits, and the worker and daemon-gateway wait forever on
// "Waiting for database migrations to be applied by api-server".
//
// Roll-forward repair, both halves of which this test pins:
//   - 20260925000000 is idempotent, so it succeeds against its own objects.
//   - a later migration recreates temporal_payload_blobs IF NOT EXISTS.
const (
	payloadBlobsVersion int64 = 20260923000000
	draftStatusVersion  int64 = 20260924000000
	accessTokensVersion int64 = 20260925000000

	// activeDaemonIDRestoreVersion is
	// 20260928193449_restore_chats_active_daemon_id.sql, the repair for the
	// earlier 20260321000000 collision.
	activeDaemonIDRestoreVersion int64 = 20260928193449
)

// rewindToRenumberWindow turns a fully migrated test database into the state a
// database that ran main between #293 and #294 is actually in.
//
// The template is already migrated, so the window state is reached by undoing
// what that database never got rather than by replaying history:
//   - drop temporal_payload_blobs — its version was consumed by the
//     access-tokens SQL, so that database never created it;
//   - undo the objects of every migration after 20260924000000, none of which
//     had been written when the window closed, and forget their goose rows.
//
// Undoing the OBJECTS as well as the version rows is what makes this faithful.
// Deleting the rows alone would leave 20260926000001's column in place, and
// its plain `ALTER TABLE projects ADD COLUMN` would then fail — a failure
// manufactured by the rewind, not one a real window database can hit, since it
// never ran that migration either.
//
// access_tokens, the dropped daemon_pats and the dropped
// connector_grants.token_hash all stay exactly as the access-tokens SQL left
// them — which is what that database has.
func rewindToRenumberWindow(t *testing.T, raw *sql.DB) {
	t.Helper()

	_, err := raw.Exec(`DROP TABLE IF EXISTS temporal_payload_blobs`)
	require.NoError(t, err)

	// 20260926000000_add_workflow_owner_user_id,
	// 20260926000001_add_forge_project_name_to_projects, and
	// 20261003213733_add_triggers (trigger_events first — it references
	// triggers).
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_workflows_owner_user_id`,
		`ALTER TABLE workflows DROP COLUMN IF EXISTS owner_user_id`,
		`ALTER TABLE projects DROP COLUMN IF EXISTS forge_project_name`,
		// 20261003235633_chat_launch_origin makes chats_with_activity depend on
		// trigger_events, so the view has to go before the table can. Replaying
		// 20261003220140 and 20261003235633 (both DROP VIEW IF EXISTS + CREATE
		// VIEW) rebuilds it, so neither needs undoing beyond this.
		`DROP VIEW IF EXISTS chats_with_activity`,
		`DROP TABLE IF EXISTS trigger_events`,
		// 20261005025048_trigger_inbound_sources: a plain CREATE TABLE that
		// references triggers, so it goes first and is recreated by replay.
		`DROP TABLE IF EXISTS trigger_registrations`,
		`DROP TABLE IF EXISTS triggers`,
		// Only migrations whose SQL would FAIL on a second run — a plain
		// ADD COLUMN or CREATE TABLE — have to be undone to keep the rewind
		// from manufacturing a failure. The view migrations are idempotent.
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	_, err = raw.Exec(
		fmt.Sprintf(`DELETE FROM %s WHERE version_id > $1`, goose.TableName()), //nolint:gosec // goose.TableName is a compile-time constant
		draftStatusVersion,
	)
	require.NoError(t, err)

	// Precondition, asserted rather than assumed: the window database records
	// 20260923000000 applied while holding the access-tokens objects and no
	// payload-blobs table. If this drifts, the rest of the test proves nothing.
	require.True(t, tableExistsInDB(t, raw, "access_tokens"),
		"the window database has access_tokens — the access-tokens SQL is what ran as 20260923000000")
	require.False(t, tableExistsInDB(t, raw, "temporal_payload_blobs"),
		"the window database never got temporal_payload_blobs — its version was consumed")
	require.True(t, versionApplied(t, raw, payloadBlobsVersion),
		"goose recorded 20260923000000 applied even though the payload-blobs SQL never ran")
	require.False(t, versionApplied(t, raw, accessTokensVersion))
}

func tableExistsInDB(t *testing.T, raw *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, raw.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists))
	return exists
}

func versionApplied(t *testing.T, raw *sql.DB, version int64) bool {
	t.Helper()
	var applied bool
	require.NoError(t, raw.QueryRow(
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE version_id = $1 AND is_applied)`, goose.TableName()), //nolint:gosec // goose.TableName is a compile-time constant
		version,
	).Scan(&applied))
	return applied
}

// TestRenumberWindowDatabaseMigratesOnPlainStartup is the whole promise: a
// window database converges with no manual SQL, just by starting api-server.
//
// It runs the real startup path (RunMigrations), not a hand-picked subset, so
// it fails the same way the running server does.
func TestRenumberWindowDatabaseMigratesOnPlainStartup(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rewindToRenumberWindow(t, raw)

	require.NoError(t, RunMigrations(raw),
		"a database that ran main inside the #293/#294 renumber window must migrate on plain startup")

	assert.True(t, tableExistsInDB(t, raw, "access_tokens"),
		"access_tokens must survive the repair intact")
	assert.True(t, tableExistsInDB(t, raw, "temporal_payload_blobs"),
		"temporal_payload_blobs must be restored: claimcheck, serverworker and accountpurge all read it")

	// Converged means nothing is left pending, not merely that Up returned.
	pending, err := PendingMigrations(raw)
	require.NoError(t, err)
	assert.Empty(t, pending, "the window database must have no migrations left pending")
}

// TestRenumberWindowRepairPreservesAccessTokens pins that the idempotent
// rewrite of 20260925000000 repairs the schema without touching the rows a
// window database has already accumulated. Re-running CREATE TABLE as
// IF NOT EXISTS is only safe if it also does not re-run the destructive
// statements against live data.
func TestRenumberWindowRepairPreservesAccessTokens(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rewindToRenumberWindow(t, raw)

	_, err := raw.Exec(`INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes)
		VALUES ('tok-window', 'org-window', 'minted before the repair', 'hash-window', 'rlat_win', ARRAY['reliant:api'])`)
	require.NoError(t, err)

	require.NoError(t, RunMigrations(raw))

	var name string
	require.NoError(t, raw.QueryRow(`SELECT name FROM access_tokens WHERE id = 'tok-window'`).Scan(&name),
		"the repair must not drop or recreate access_tokens — live tokens would stop working")
	assert.Equal(t, "minted before the repair", name)
}

// TestAprilRenumberWindowRestoresActiveDaemonID covers the SECOND collision,
// found while auditing for the first.
//
// Version 20260321000000 was claimed by add_metadata_to_yields (#76) and then
// by add_active_daemon_id_to_chats (#77) before #82 renumbered the former to
// 20260321000001. A database migrated between #76 and #77 recorded that
// version having run the YIELDS SQL, so goose reports the active_daemon_id
// migration as applied and its ALTER never runs — chats loses a column that
// internal/db/core/chat.go reads on every chat.
//
// Reproducing it needs the VIEW dropped as well as the column, and that is the
// point rather than a detail of the setup. chats_with_activity is
// `SELECT c.*, ... FROM chats c`, and Postgres expands `c.*` once at CREATE
// VIEW time; a view created without the column never gains it, and every later
// CREATE OR REPLACE keeps the frozen column list. So the window database is
// missing it in BOTH places, and the generated queries read it from the view.
func TestAprilRenumberWindowRestoresActiveDaemonID(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	_, err := raw.Exec(`DROP VIEW IF EXISTS chats_with_activity`)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE chats DROP COLUMN IF EXISTS active_daemon_id`)
	require.NoError(t, err)

	// The view as that database has it: rebuilt by a later migration, from a
	// chats table with no active_daemon_id, so the column is absent from it.
	_, err = raw.Exec(`CREATE VIEW chats_with_activity AS
		SELECT c.*, (SELECT MAX(m.created_at) FROM messages m WHERE m.chat_id = c.id) AS last_message_at,
		       0 AS activity
		FROM chats c`)
	require.NoError(t, err)

	_, err = raw.Exec(
		fmt.Sprintf(`DELETE FROM %s WHERE version_id = $1`, goose.TableName()), //nolint:gosec // goose.TableName is a compile-time constant
		activeDaemonIDRestoreVersion,
	)
	require.NoError(t, err)

	require.NoError(t, RunMigrations(raw),
		"a database from the April 20260321000000 collision must migrate on plain startup")

	var onTable bool
	require.NoError(t, raw.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'chats' AND column_name = 'active_daemon_id')`).Scan(&onTable))
	assert.True(t, onTable, "chats.active_daemon_id must be restored")

	var onView bool
	require.NoError(t, raw.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'chats_with_activity' AND column_name = 'active_daemon_id')`).Scan(&onView))
	assert.True(t, onView,
		"the view must expose active_daemon_id: the generated chat queries select it FROM chats_with_activity, "+
			"so restoring the column without re-expanding the view leaves every chat read broken")

	// The read the application actually performs.
	_, err = raw.Exec(`SELECT active_daemon_id FROM chats_with_activity LIMIT 1`)
	assert.NoError(t, err)
}

// TestPayloadBlobsRestoreIsANoOpOnAHealthyDatabase covers the other population:
// every database that is NOT in the window already has temporal_payload_blobs,
// and the restore migration must be inert there. The template is such a
// database, so running the startup path against it unchanged is the check.
func TestPayloadBlobsRestoreIsANoOpOnAHealthyDatabase(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	require.NoError(t, RunMigrations(raw))

	require.True(t, tableExistsInDB(t, raw, "temporal_payload_blobs"))

	// The index from the original 20260923000000 must still be the one in
	// place — a restore that dropped and rebuilt it would be rewriting an
	// index on a live table for no reason.
	var indexes int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM pg_indexes
		WHERE tablename = 'temporal_payload_blobs' AND indexname = 'idx_temporal_payload_blobs_last_ref'`).Scan(&indexes))
	assert.Equal(t, 1, indexes)
}
