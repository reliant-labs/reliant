// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Deleting a project with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3, D18): scheduling hides the
// project and its chats and pauses its triggers; restoring undoes exactly
// that; the purge deletes it once the window has passed, once.

type deleteFixture struct {
	repo                       *Repo
	ctx                        context.Context
	owner, project, chat       string
	enabledTrigger, userPaused string
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	suffix := uuid.NewString()[:8]
	f := &deleteFixture{
		repo: repo, ctx: context.Background(),
		owner: "owner-" + suffix, project: "proj-" + suffix, chat: "chat-" + suffix,
		enabledTrigger: "trig-on-" + suffix, userPaused: "trig-off-" + suffix,
	}
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.CreateProject(f.ctx, &Project{ID: f.project, UserID: f.owner, Name: "doomed", Path: "/home/me/doomed-" + suffix,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, repo.CreateChat(f.ctx, &Chat{ID: f.chat, UserID: f.owner, ProjectID: f.project, Title: "a chat",
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	for _, tr := range []struct {
		id      string
		enabled bool
	}{{f.enabledTrigger, true}, {f.userPaused, false}} {
		require.NoError(t, repo.CreateTrigger(f.ctx, &core.Trigger{
			ID: tr.id, UserID: f.owner, ProjectID: f.project, Name: tr.id, Kind: core.TriggerKindSchedule, Enabled: tr.enabled,
			Workflow: "builtin://agent", Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now,
		}))
	}
	return f
}

func (f *deleteFixture) trigger(t *testing.T, id string) *core.Trigger {
	t.Helper()
	tr, err := f.repo.GetTrigger(f.ctx, id)
	require.NoError(t, err)
	return tr
}

func (f *deleteFixture) listed(t *testing.T) (projects, chats bool) {
	t.Helper()
	ps, err := f.repo.ListProjects(f.ctx, ProjectFilters{UserID: f.owner})
	require.NoError(t, err)
	cs, err := f.repo.ListChats(f.ctx, ChatFilters{UserID: f.owner, Limit: 100})
	require.NoError(t, err)
	return len(ps) == 1, len(cs) == 1
}

func TestProjectDeleteScheduleHidesAndRestoreBringsBack(t *testing.T) {
	f := newDeleteFixture(t)
	after := time.Now().Add(core.ProjectDeleteWindow).UTC().Truncate(time.Second)

	got, err := f.repo.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after)
	require.NoError(t, err)
	assert.False(t, got.AlreadyScheduled)
	assert.True(t, got.DeleteAfter.Equal(after))
	assert.Equal(t, []string{f.enabledTrigger}, got.PausedTriggerIDs, "only the enabled trigger is paused")

	// Hidden: no list shows it or its chat, and its owner's checked read is
	// NotFound.
	projects, chats := f.listed(t)
	assert.False(t, projects, "a scheduled project leaves the project list")
	assert.False(t, chats, "its chats leave the chat list")
	_, err = f.repo.GetProjectWithUserCheck(f.ctx, f.project, f.owner)
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	deleted, err := f.repo.ListDeletedProjects(f.ctx, f.owner, nil)
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	assert.Equal(t, f.owner, *deleted[0].DeletedBy)

	on := f.trigger(t, f.enabledTrigger)
	assert.False(t, on.Enabled)
	assert.Equal(t, core.TriggerPausedProjectDeleted, on.PausedReason)
	off := f.trigger(t, f.userPaused)
	assert.False(t, off.Enabled)
	assert.Empty(t, off.PausedReason, "a trigger a person paused keeps its own pause")

	// A second delete keeps the first date and changes nothing.
	again, err := f.repo.ScheduleProjectDeletion(f.ctx, f.project, "someone-else", after.Add(48*time.Hour))
	require.NoError(t, err)
	assert.True(t, again.AlreadyScheduled)
	assert.True(t, again.DeleteAfter.Equal(after))
	assert.Empty(t, again.PausedTriggerIDs)

	resumed, err := f.repo.RestoreProject(f.ctx, f.project)
	require.NoError(t, err)
	assert.Equal(t, []string{f.enabledTrigger}, resumed)
	projects, chats = f.listed(t)
	assert.True(t, projects)
	assert.True(t, chats)
	p, err := f.repo.GetProjectWithUserCheck(f.ctx, f.project, f.owner)
	require.NoError(t, err)
	assert.Nil(t, p.DeleteAfter)
	assert.Nil(t, p.DeletedBy)
	assert.True(t, f.trigger(t, f.enabledTrigger).Enabled, "restore turns the trigger the delete paused back on")
	assert.False(t, f.trigger(t, f.userPaused).Enabled, "and leaves the one a person paused alone")

	// Restoring again is a no-op.
	resumed, err = f.repo.RestoreProject(f.ctx, f.project)
	require.NoError(t, err)
	assert.Empty(t, resumed)
}

func TestProjectPurgeRunsAfterTheWindowOnce(t *testing.T) {
	f := newDeleteFixture(t)
	after := time.Now().Add(core.ProjectDeleteWindow).UTC()
	_, err := f.repo.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after)
	require.NoError(t, err)

	// Inside the window: nothing is due.
	res, err := f.repo.PurgeDueProjects(f.ctx, time.Now(), time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.NoError(t, err, "a project inside its window is kept")

	// After it: purged, with everything it owned.
	later := after.Add(time.Minute)
	res, err = f.repo.PurgeDueProjects(f.ctx, later, time.Hour, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged)
	assert.Zero(t, res.Failed)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err, "the purged project is gone")
	_, err = f.repo.GetTrigger(f.ctx, f.enabledTrigger)
	assert.ErrorIs(t, err, core.ErrTriggerNotFound)
	_, err = f.repo.GetChat(f.ctx, f.chat)
	assert.Error(t, err, "its chat is gone")

	// Idempotent: a second pass finds nothing, and so does a restore.
	res, err = f.repo.PurgeDueProjects(f.ctx, later, time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged)
	_, err = f.repo.RestoreProject(f.ctx, f.project)
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
}

func TestProjectRestoreIsRefusedOncePurgeBegins(t *testing.T) {
	f := newDeleteFixture(t)
	after := time.Now().Add(-time.Minute).UTC() // already due
	_, err := f.repo.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after)
	require.NoError(t, err)

	// A claim another replica holds: this one skips the project, and a
	// restore is refused, so it can never come back half-purged.
	_, claimed, err := claimPurge(f, time.Now())
	require.NoError(t, err)
	require.True(t, claimed)
	res, err := f.repo.PurgeDueProjects(f.ctx, time.Now(), time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "a project another purge holds is left to it")
	_, err = f.repo.RestoreProject(f.ctx, f.project)
	assert.ErrorIs(t, err, core.ErrProjectPurgeStarted)
	deleted, err := f.repo.ListDeletedProjects(f.ctx, f.owner, nil)
	require.NoError(t, err)
	assert.Empty(t, deleted, "a project being purged is not offered for restore")

	// Its claim expires (the replica died): the next purge finishes it.
	res, err = f.repo.PurgeDueProjects(f.ctx, time.Now().Add(2*time.Hour), time.Hour, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged)
}

func TestPurgeProjectNowDeletesAtOnce(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.repo.PurgeProjectNow(f.ctx, f.project, "not-the-owner")
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	triggers, err := f.repo.PurgeProjectNow(f.ctx, f.project, f.owner)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{f.enabledTrigger, f.userPaused}, triggers)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err)
}

// The rows that name a project with no foreign key to it go with its purge,
// and those an older delete left behind are swept.
func TestProjectPurgeCleansRowsWithNoForeignKey(t *testing.T) {
	f := newDeleteFixture(t)
	seedRecovery := func(id, projectID string) {
		t.Helper()
		_, err := f.repo.ORM().Bun().NewRaw(`INSERT INTO worktree_recoveries
			(id, owner_user_id, source_worktree_id, project_id, daemon_id, capture_id, fence, activity_generation, observation_id, manifest_digest, idempotency_key)
			VALUES (?0, ?1, ?0, ?2, 'd', ?0, 'f', 0, 'o', 'm', ?0)`, id, f.owner, projectID).Exec(f.ctx)
		require.NoError(t, err)
		_, err = f.repo.ORM().Bun().NewRaw(`INSERT INTO worktree_recovery_checkouts
			(recovery_id, repo_id, rel, remote_url, recovery_ref, commit_oid, manifest_digest, descriptor_json)
			VALUES (?0, 'r', '.', 'https://example.com/x.git', ?0, 'c', 'm', '{}')`, id).Exec(f.ctx)
		require.NoError(t, err)
	}
	count := func(table, id string) int {
		t.Helper()
		col := "id"
		if table == "worktree_recovery_checkouts" {
			col = "recovery_id"
		}
		var n int
		require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM `+table+` WHERE `+col+` = ?`, id).Scan(f.ctx, &n))
		return n
	}
	mine := "recovery-" + uuid.NewString()[:8]
	orphan := "recovery-" + uuid.NewString()[:8]
	seedRecovery(mine, f.project)
	seedRecovery(orphan, "project-deleted-long-ago-"+uuid.NewString()[:8])

	_, err := f.repo.PurgeProjectNow(f.ctx, f.project, f.owner)
	require.NoError(t, err)
	assert.Zero(t, count("worktree_recoveries", mine), "the purge removes the project's recoveries")
	assert.Zero(t, count("worktree_recovery_checkouts", mine))
	assert.Equal(t, 1, count("worktree_recoveries", orphan), "the purge touches only its own project's rows")

	n, err := f.repo.SweepOrphanedProjectRows(f.ctx, 500)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	assert.Zero(t, count("worktree_recoveries", orphan), "the sweep removes what an older delete left")
	assert.Zero(t, count("worktree_recovery_checkouts", orphan))
}

func claimPurge(f *deleteFixture, now time.Time) (string, bool, error) {
	ids, err := f.repo.ListDueProjectPurges(f.ctx, now, time.Hour, 10)
	if err != nil || len(ids) == 0 {
		return "", false, err
	}
	return store.ClaimProjectPurge(f.ctx, f.repo.ORM(), ids[0], now, time.Hour)
}
