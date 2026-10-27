// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Reclaim on merge, the database half: what the chats working in a worktree
// say about it, the archive that leaves those chats live, and the revive
// their next tool batch makes.

// landedFixture is a worktree with one chat bound to it, both quiet for an
// hour.
func landedFixture(t *testing.T, env *storetest.Env, id string) *core.Chat {
	t.Helper()
	c := boundFixture(t, env, id, false)
	mustExecConv(t, env, `UPDATE chats SET last_active = NOW() - interval '1 hour' WHERE id = $1`, c.ID)
	mustExecConv(t, env, `UPDATE worktrees SET last_active = NOW() - interval '1 hour', created_at = NOW() - interval '2 hours' WHERE id = $1`, *c.WorktreeID)
	return c
}

func TestWorktreeRunActivities(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	quiet := landedFixture(t, env, "quiet")
	running := landedFixture(t, env, "running")
	convWorkflow(t, env, "wf-running", running.ID, core.WorkflowStateActive, core.StopReasonUnspecified)
	paused := landedFixture(t, env, "paused")
	convWorkflow(t, env, "wf-paused", paused.ID, core.WorkflowStateStopped, core.StopReasonPaused)
	done := landedFixture(t, env, "done")
	convWorkflow(t, env, "wf-done", done.ID, core.WorkflowStateStopped, core.StopReasonCompleted)
	mustExecConv(t, env, `UPDATE workflows SET created_at = NOW() - interval '3 hours', completed_at = NOW() - interval '10 minutes' WHERE id = 'wf-done'`)

	// A sub-agent's worktree: no chat is bound to it, a thread of a running
	// chat is.
	spawned := &core.Worktree{ID: "spawned", ProjectID: storetest.SeedProjectID, Name: "spawned", Path: "/tmp/spawned", Branch: "spawned"}
	require.NoError(t, env.Repo.CreateWorktree(ctx, spawned))
	mustExecConv(t, env, `INSERT INTO threads (id, chat_id, created_at, origin, status, worktree_id) VALUES ('t-sub', $1, NOW(), 'spawn', 2, 'spawned')`, running.ID)

	acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{*quiet.WorktreeID, *running.WorktreeID, *paused.WorktreeID, *done.WorktreeID, "spawned", "missing"})
	require.NoError(t, err)
	require.False(t, acts[*quiet.WorktreeID].Running)
	require.WithinDuration(t, time.Now().Add(-time.Hour), acts[*quiet.WorktreeID].LastActive, 2*time.Minute)
	require.True(t, acts[*running.WorktreeID].Running, "an active run")
	require.True(t, acts[*paused.WorktreeID].Running, "a paused run is live: it resumes")
	require.False(t, acts[*done.WorktreeID].Running, "a completed run is over")
	require.WithinDuration(t, time.Now().Add(-10*time.Minute), acts[*done.WorktreeID].LastActive, 2*time.Minute,
		"the run's stop is later than the chat's own last_active")
	require.True(t, acts["spawned"].Running, "a thread working in a worktree counts")
	_, ok := acts["missing"]
	require.False(t, ok)
}

func TestArchiveLandedWorktree_OnlyWhenQuietAndNotRunning(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "landed")
	id := *c.WorktreeID

	ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now().Add(-3*time.Hour))
	require.NoError(t, err)
	require.False(t, ok, "touched an hour ago, three hours of quiet required")

	convWorkflow(t, env, "wf", c.ID, core.WorkflowStateActive, core.StopReasonUnspecified)
	mustExecConv(t, env, `UPDATE workflows SET created_at = NOW() - interval '1 day'`)
	ok, err = store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "a chat run is going")

	mustExecConv(t, env, `UPDATE workflows SET state = 3, stop_reason = 1, completed_at = NOW() - interval '30 minutes'`)
	ok, err = store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt)
	require.NotNil(t, wt.CleanupMetadata)
	require.Equal(t, core.ArchivedBecauseLanded, wt.CleanupMetadata.ArchivedBecause)
	chat, err := env.Repo.GetChat(ctx, c.ID)
	require.NoError(t, err)
	require.NotEqual(t, core.ChatStateArchived, chat.State, "the chat stays live")

	ok, err = store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "already archived")
}

func TestArchiveLandedWorktree_NeverMain(t *testing.T) {
	env := storetest.New(t)
	c := boundFixture(t, env, "main", true)
	ok, err := store.ArchiveLandedWorktree(context.Background(), env.ORM, *c.WorktreeID, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReviveArchivedWorktree(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "revive")
	id := *c.WorktreeID

	fence, err := store.ReviveArchivedWorktree(ctx, env.ORM, id)
	require.NoError(t, err)
	require.Empty(t, fence, "a live worktree has nothing to retire")

	ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, id, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, env.Repo.MergeWorktreeCleanupMetadata(ctx, id, func(m *core.CleanupMetadata, _ bool) bool {
		m.DirectoryDeleted, m.HeldReason, m.StrippedBytes = true, "dirty", 42
		return true
	}))
	archived, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)

	fence, err = store.ReviveArchivedWorktree(ctx, env.ORM, id)
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(archived.DeletedAt.UnixNano(), 10), fence, "the fence is the archive's, as the sweep derives it")
	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.Nil(t, wt.DeletedAt)
	m := wt.CleanupMetadata
	require.NotNil(t, m)
	require.Equal(t, fence, m.RetireFence, "left for the sweep to deliver until the daemon re-locks")
	require.False(t, m.DirectoryDeleted)
	require.Empty(t, m.HeldReason)
	require.Empty(t, m.ArchivedBecause)
	require.EqualValues(t, 42, m.StrippedBytes, "what was shed is remembered")
	again, err := store.ReviveArchivedWorktree(ctx, env.ORM, id)
	require.NoError(t, err)
	require.Equal(t, fence, again, "a concurrent/second batch must also retire the pending fence")
}

func TestReviveArchivedWorktree_NameTaken(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "taken")
	ok, err := store.ArchiveLandedWorktree(ctx, env.ORM, *c.WorktreeID, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, env.Repo.CreateWorktree(ctx, &core.Worktree{ID: "taken-2", ProjectID: storetest.SeedProjectID, Name: "taken", Path: "/tmp/taken-2", Branch: "taken-2"}))

	_, err = store.ReviveArchivedWorktree(ctx, env.ORM, *c.WorktreeID)
	require.True(t, errors.Is(err, core.ErrWorktreeNameTaken), "got %v", err)
	wt, err := env.Repo.GetWorktree(ctx, *c.WorktreeID)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt, "a failed revive leaves the row archived")
}
