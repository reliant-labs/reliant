// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerMigrationVersion is 20260926000000_add_workflow_owner_user_id.sql.
const ownerMigrationVersion int64 = 20260926000000

// rewindOwnerMigration puts a migrated test database back into the state a
// deployed database is in just before the owner migration runs: the version is
// unrecorded and the column does not exist. Everything seeded afterwards is
// "pre-existing data" from the migration's point of view.
func rewindOwnerMigration(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.Exec(`DROP INDEX IF EXISTS idx_workflows_owner_user_id`)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE workflows DROP COLUMN IF EXISTS owner_user_id`)
	require.NoError(t, err)
	_, err = raw.Exec(fmt.Sprintf(`DELETE FROM %s WHERE version_id = $1`, goose.TableName()), ownerMigrationVersion) //nolint:gosec // goose.TableName is a compile-time constant
	require.NoError(t, err)
}

// applyOwnerMigration runs the real embedded migration through goose, exactly as
// RunMigrations does at startup.
func applyOwnerMigration(t *testing.T, raw *sql.DB) {
	t.Helper()
	require.NoError(t, initGoose())
	require.NoError(t, goose.UpTo(raw, migrationsDir, ownerMigrationVersion, goose.WithAllowMissing()))
}

// seedPreOwnerRuns inserts runs the old way — no owner column in the INSERT —
// and returns (ownedRunIDs, orphanRunID). The orphan's chat does not exist,
// which workflows.chat_id permits: it carries no foreign key.
func seedPreOwnerRuns(t *testing.T, raw *sql.DB, userID string, runs int) ([]string, string) {
	t.Helper()
	chatID := "owner-mig-chat-" + userID
	_, err := raw.Exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		VALUES ($1, 't', 'test-project', $2, now(), now(), now())`, chatID, userID)
	require.NoError(t, err)

	ids := make([]string, 0, runs)
	for i := 0; i < runs; i++ {
		id := fmt.Sprintf("owner-mig-run-%s-%03d", userID, i)
		_, err := raw.Exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at)
			VALUES ($1, $2, 'builtin://agent', $1, now())`, id, chatID)
		require.NoError(t, err)
		ids = append(ids, id)
	}

	orphanID := "owner-mig-orphan-" + userID
	_, err = raw.Exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at)
		VALUES ($1, 'owner-mig-deleted-chat', 'builtin://agent', $1, now())`, orphanID)
	require.NoError(t, err)
	return ids, orphanID
}

func ownerOf(t *testing.T, raw *sql.DB, runID string) sql.NullString {
	t.Helper()
	var owner sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT owner_user_id FROM workflows WHERE id = $1`, runID).Scan(&owner))
	return owner
}

// TestOwnerMigration_BackfillsFromChatAndLeavesOrphansNull pins what the
// backfill promises for data that existed before it: every run whose chat
// exists gets that chat's user, and a run whose chat is gone keeps NULL
// rather than blocking the migration or being handed an invented owner.
func TestOwnerMigration_BackfillsFromChatAndLeavesOrphansNull(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rewindOwnerMigration(t, raw)
	owned, orphan := seedPreOwnerRuns(t, raw, "user-alice", 3)

	applyOwnerMigration(t, raw)

	for _, id := range owned {
		got := ownerOf(t, raw, id)
		require.True(t, got.Valid, "run %s was not backfilled", id)
		assert.Equal(t, "user-alice", got.String)
	}
	assert.False(t, ownerOf(t, raw, orphan).Valid,
		"a run whose chat is gone must keep a NULL owner, not an invented one")

	var valid bool
	require.NoError(t, raw.QueryRow(`SELECT indisvalid FROM pg_index
		WHERE indexrelid = 'idx_workflows_owner_user_id'::regclass`).Scan(&valid))
	assert.True(t, valid, "the owner index must be built and valid")
}

// TestOwnerMigration_ResumesAfterACrashMidway covers a deploy that died after
// the column was added and part of the table was backfilled, before goose
// recorded the version. The re-run must finish the job, must not clobber an
// owner the application already stamped on a new row, and must replace an
// INVALID index (what a failed CREATE INDEX CONCURRENTLY leaves behind) rather
// than accept it as done.
func TestOwnerMigration_ResumesAfterACrashMidway(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rewindOwnerMigration(t, raw)
	owned, orphan := seedPreOwnerRuns(t, raw, "user-bob", 4)

	// The half-finished state: column present, first run backfilled, a run
	// written by the new code with its own owner, and an index Postgres marked
	// invalid.
	_, err := raw.Exec(`ALTER TABLE workflows ADD COLUMN owner_user_id text`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE workflows SET owner_user_id = 'user-bob' WHERE id = $1`, owned[0])
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE workflows SET owner_user_id = 'stamped-by-app' WHERE id = $1`, owned[1])
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX idx_workflows_owner_user_id ON workflows (owner_user_id) WHERE owner_user_id IS NOT NULL`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'idx_workflows_owner_user_id'::regclass`)
	require.NoError(t, err)

	applyOwnerMigration(t, raw)

	assert.Equal(t, "user-bob", ownerOf(t, raw, owned[0]).String)
	assert.Equal(t, "stamped-by-app", ownerOf(t, raw, owned[1]).String,
		"the backfill must never overwrite an owner the application already wrote")
	for _, id := range owned[2:] {
		got := ownerOf(t, raw, id)
		require.True(t, got.Valid, "run %s was not backfilled on re-run", id)
		assert.Equal(t, "user-bob", got.String)
	}
	assert.False(t, ownerOf(t, raw, orphan).Valid)

	var valid bool
	require.NoError(t, raw.QueryRow(`SELECT indisvalid FROM pg_index
		WHERE indexrelid = 'idx_workflows_owner_user_id'::regclass`).Scan(&valid))
	assert.True(t, valid, "a re-run must rebuild an invalid index, not keep it")
}

// TestOwnerMigration_BackfillDoesNotHoldTheTableLock pins the property that
// makes this migration safe to deploy against a live engine.
//
// workflows is written by every running run. Run as one transaction, the
// ALTER's ACCESS EXCLUSIVE lock would be held until the backfill committed —
// measured at ~20s on 2M rows, during which every SELECT, INSERT and UPDATE on
// workflows blocked. So the backfill must run with that lock already released.
//
// Asserted directly rather than by timing: a statement trigger on workflows
// records, for every UPDATE the backfill issues, whether the migrating session
// still holds an AccessExclusiveLock on the table at that moment.
func TestOwnerMigration_BackfillDoesNotHoldTheTableLock(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	rewindOwnerMigration(t, raw)
	seedPreOwnerRuns(t, raw, "user-carol", 2)

	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE owner_mig_lock_probe (held_exclusive boolean NOT NULL)`,
		`CREATE FUNCTION owner_mig_record_lock() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			INSERT INTO owner_mig_lock_probe
			SELECT EXISTS (
				SELECT 1 FROM pg_locks
				WHERE pid = pg_backend_pid()
				  AND relation = 'workflows'::regclass
				  AND mode = 'AccessExclusiveLock'
				  AND granted
			);
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER owner_mig_record_lock AFTER UPDATE ON workflows
			FOR EACH STATEMENT EXECUTE FUNCTION owner_mig_record_lock()`,
	} {
		_, err := raw.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	applyOwnerMigration(t, raw)

	var updates, heldExclusive int
	require.NoError(t, raw.QueryRow(`SELECT count(*), count(*) FILTER (WHERE held_exclusive)
		FROM owner_mig_lock_probe`).Scan(&updates, &heldExclusive))
	require.Positive(t, updates, "the probe saw no backfill UPDATE — this test is checking nothing")
	assert.Zero(t, heldExclusive,
		"the backfill ran while holding ACCESS EXCLUSIVE on workflows; "+
			"every live run would block for its whole duration")
}
