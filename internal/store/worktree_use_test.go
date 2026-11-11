// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The use a worktree's rows did not show on 2026-11-10: a run held on a
// usage limit, and an agent working in it by path from a chat bound
// elsewhere (recorded by NoteWorktreeUse from the daemon's report).

// providerWait parks thread of chatID on a provider wait that resumes at
// resumeAt (store.RecordUsageLimitWait's row).
func providerWait(t *testing.T, env *storetest.Env, chatID, threadID string, resumeAt time.Time) {
	t.Helper()
	mustExecConv(t, env, `INSERT INTO provider_backoff (thread_id, chat_id, waiting_since, resume_at, reason, updated_at)
		VALUES ($1, $2, NOW() - interval '2 hours', $3, $4, NOW())`, threadID, chatID, resumeAt.UTC(), core.ReasonUsageLimit)
}

func TestWorktreeRunActivities_UsageLimitWaitIsRunning(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	// The run stopped on the limit (a sub-agent's run ends failed, and the
	// next message resumes it): its workflow row says nothing is running.
	held := landedFixture(t, env, "held")
	convWorkflow(t, env, "wf-held", held.ID, core.WorkflowStateStopped, core.StopReasonFailed)
	mustExecConv(t, env, `UPDATE workflows SET created_at = NOW() - interval '3 hours', completed_at = NOW() - interval '2 hours'`)
	resume := time.Now().Add(5 * time.Hour)
	providerWait(t, env, held.ID, "wf-held", resume)

	// A marker a dead run left behind, its resume time long past.
	stale := landedFixture(t, env, "stale")
	providerWait(t, env, stale.ID, "t-stale", time.Now().Add(-30*time.Minute))

	// A cleared wait: waiting_since is NULL.
	cleared := landedFixture(t, env, "cleared")
	providerWait(t, env, cleared.ID, "t-cleared", time.Now().Add(time.Hour))
	mustExecConv(t, env, `UPDATE provider_backoff SET waiting_since = NULL, resume_at = NULL WHERE thread_id = 't-cleared'`)

	ids := []string{*held.WorktreeID, *stale.WorktreeID, *cleared.WorktreeID}
	acts, err := store.WorktreeRunActivities(ctx, env.ORM, ids)
	require.NoError(t, err)
	require.True(t, acts[*held.WorktreeID].Running, "a run held on a usage limit resumes in it")
	require.WithinDuration(t, resume, acts[*held.WorktreeID].LastActive, time.Second,
		"and it is in use until the wait's resume time")
	require.False(t, acts[*stale.WorktreeID].Running, "a wait past its resume time holds nothing")
	require.WithinDuration(t, time.Now().Add(-30*time.Minute), acts[*stale.WorktreeID].LastActive, 2*time.Minute,
		"its resume time only ages the worktree's last activity")
	require.False(t, acts[*cleared.WorktreeID].Running)
	require.WithinDuration(t, time.Now().Add(-time.Hour), acts[*cleared.WorktreeID].LastActive, 2*time.Minute,
		"a cleared wait is not activity")

	running, err := env.Repo.ListWorktreeIDsWithRunningChat(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{*held.WorktreeID: true}, running)

	ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, *held.WorktreeID, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "never archived under a run waiting out a usage limit")
}

func TestWorktreeRunActivities_UsageLimitWaitOfSpawnedThread(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	parent := landedFixture(t, env, "parent")
	spawned := &core.Worktree{ID: "spawned", ProjectID: storetest.SeedProjectID, Name: "spawned", Path: "/tmp/spawned", Branch: "spawned"}
	require.NoError(t, env.Repo.CreateWorktree(ctx, spawned))
	mustExecConv(t, env, `UPDATE worktrees SET last_active = NOW() - interval '1 hour', created_at = NOW() - interval '2 hours' WHERE id = 'spawned'`)
	mustExecConv(t, env, `INSERT INTO threads (id, chat_id, created_at, origin, status, worktree_id) VALUES ('t-sub', $1, NOW(), 'spawn', 2, 'spawned')`, parent.ID)
	providerWait(t, env, parent.ID, "t-sub", time.Now().Add(time.Hour))

	running, err := env.Repo.ListWorktreeIDsWithRunningChat(ctx, []string{"spawned"})
	require.NoError(t, err)
	require.True(t, running["spawned"], "a sub-agent's wait holds the worktree it was spawned into")
}

func TestNoteWorktreeUse(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "used")
	id := *c.WorktreeID
	lastActive := func() time.Time {
		wt, err := env.Repo.GetWorktree(ctx, id)
		require.NoError(t, err)
		return wt.LastActive
	}

	// The fixture's rows were last active an hour ago, so 20 minutes of
	// quiet would archive it; the daemon saw it used 10 minutes ago.
	used := time.Now().Add(-10 * time.Minute)
	require.NoError(t, store.NoteWorktreeUse(ctx, env.ORM, id, used))
	require.WithinDuration(t, used, lastActive(), time.Millisecond)

	ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now().Add(-20*time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "the daemon saw it used 10 minutes ago")

	require.NoError(t, store.NoteWorktreeUse(ctx, env.ORM, id, used.Add(-time.Hour)))
	require.WithinDuration(t, used, lastActive(), time.Millisecond, "never lowered")

	require.NoError(t, store.NoteWorktreeUse(ctx, env.ORM, id, time.Now().Add(48*time.Hour)))
	require.WithinDuration(t, time.Now(), lastActive(), time.Minute, "a fast daemon clock is taken as now")

	mustExecConv(t, env, `UPDATE worktrees SET deleted_at = NOW() WHERE id = $1`, id)
	before := lastActive()
	require.NoError(t, store.NoteWorktreeUse(ctx, env.ORM, id, time.Now().Add(time.Minute)))
	require.Equal(t, before, lastActive(), "an archived row is left alone")
}
