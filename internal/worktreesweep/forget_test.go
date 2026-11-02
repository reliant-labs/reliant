// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// folderlessSender is every connected machine answering a reconcile the way
// a machine without the folder does: gone. It records who was asked.
type folderlessSender struct {
	mu    sync.Mutex
	asked map[string]int
}

func (f *folderlessSender) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, cmd string, payload []byte, _ int32) ([]byte, error) {
	if cmd != cmdReconcile {
		panic("unexpected command " + cmd)
	}
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.asked[daemonID]++
	f.mu.Unlock()
	resp := worktreereclaim.Response{}
	for _, w := range req.Worktrees {
		resp.Results = append(resp.Results, worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeGone})
	}
	return json.Marshal(resp)
}

// An archived workspace whose machine was removed has no machine on record,
// and when no connected machine has its folder nothing will ever settle it:
// the hold used to sit in Storage for good. Once every connected machine has
// answered that it lacks the folder, the Storage view offers to forget it,
// and Forget drops the record — the row leaves Storage and the sweep — while
// deleting nothing and keeping the workspace in the archive. A connected
// machine that has not been asked yet keeps it from being offered.
func TestForget_AWorkspaceNoConnectedMachineHasIsForgottenOnceAllHaveAnswered(t *testing.T) {
	t.Parallel()
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	userID := uuid.NewString()
	first, second := "d-"+uuid.NewString(), "d-"+uuid.NewString()
	now := time.Now().UTC()
	project := &db.Project{ID: "p-" + userID, UserID: userID, Name: "proj", Path: "/home/u/proj", CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(ctx, project))
	for _, id := range []string{first, second} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	connect := func(id string) {
		require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: id, UserID: userID, Source: db.DaemonAttachmentSourceInbound}))
	}
	connect(first)

	// Its machine was removed: RemoveDaemon left the row with no owner.
	wtID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: wtID, Name: "old-spike", Path: "/home/u/.reliant/worktrees/proj/old-spike", Branch: "feat/x", BaseBranch: "main",
		ProjectID: project.ID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.ArchiveWorktree(ctx, wtID))

	sender := &folderlessSender{asked: map[string]int{}}
	s := New(repo, sender)
	require.NoError(t, s.Sweep(ctx))
	require.Equal(t, 1, sender.asked[first], "the connected machine is asked whether it has the folder")

	unowned := func() *Held {
		t.Helper()
		view, err := StorageView(ctx, repo, userID)
		require.NoError(t, err)
		for _, m := range view {
			if m.DaemonID == "" {
				require.Len(t, m.Held, 1)
				return &m.Held[0]
			}
		}
		return nil
	}
	held := unowned()
	require.NotNil(t, held, "a workspace with no machine on record is listed, under no machine")
	assert.Equal(t, string(worktreereclaim.ReasonNoMachine), held.Reason)
	assert.False(t, held.Removable, "no machine can clean it up")
	assert.True(t, held.Forgettable, "the only connected machine does not have the folder")

	// Another machine connects. Until it is asked, it may have the folder.
	connect(second)
	assert.False(t, unowned().Forgettable, "a connected machine that was not asked keeps it")
	require.ErrorIs(t, Forget(ctx, repo, userID, wtID), ErrNotForgettable)

	// Its next pass (the hold's back-off later) asks the new machine too.
	s.now = func() time.Time { return time.Now().Add(holdBackoffMax + time.Minute) }
	require.NoError(t, s.Sweep(ctx))
	require.Equal(t, 1, sender.asked[second])
	require.True(t, unowned().Forgettable, "every connected machine said it does not have the folder")

	require.ErrorIs(t, Forget(ctx, repo, "someone-else", wtID), ErrNotHeld, "only its owner may forget it")
	require.NoError(t, Forget(ctx, repo, userID, wtID))

	assert.Nil(t, unowned(), "forgotten: it leaves Storage")
	got, err := repo.GetWorktree(ctx, wtID)
	require.NoError(t, err)
	require.NotNil(t, got.DeletedAt, "it stays in the archive")
	require.NotNil(t, got.CleanupMetadata)
	assert.True(t, got.CleanupMetadata.DirectoryDeleted, "nothing tracks the folder any more")
	assert.True(t, got.CleanupMetadata.Forgotten, "and the record says it was forgotten, not deleted")
	assert.Empty(t, got.CleanupMetadata.HeldReason)

	// The sweep never asks about it again, and forgetting twice is refused.
	asked := sender.asked[first] + sender.asked[second]
	require.NoError(t, s.Sweep(ctx))
	assert.Equal(t, asked, sender.asked[first]+sender.asked[second], "a forgotten row is never sent to a machine")
	require.ErrorIs(t, Forget(ctx, repo, userID, wtID), ErrNotHeld)
}

// A row a machine has claimed is that machine's to settle: Forget never
// touches it, even with the absences it collected before the claim.
func TestForget_RefusesARowAMachineClaimed(t *testing.T) {
	t.Parallel()
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	userID := uuid.NewString()
	daemon := "d-" + uuid.NewString()
	now := time.Now().UTC()
	project := &db.Project{ID: "p-" + userID, UserID: userID, Name: "proj", Path: "/home/u/proj", CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(ctx, project))
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemon, UserID: userID}))
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{DaemonID: daemon, UserID: userID, Source: db.DaemonAttachmentSourceInbound}))
	wtID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: wtID, Name: "claimed", Path: "/home/u/.reliant/worktrees/proj/claimed", Branch: "feat/y", BaseBranch: "main",
		ProjectID: project.ID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.ArchiveWorktree(ctx, wtID))
	require.NoError(t, repo.UpdateWorktreeCleanupMetadata(ctx, wtID, &db.CleanupMetadata{
		HeldReason: string(worktreereclaim.ReasonNoMachine), AbsentOn: []string{daemon},
	}))
	recorded, err := repo.AdoptWorktreeDaemon(ctx, wtID, daemon)
	require.NoError(t, err)
	require.Equal(t, daemon, recorded)

	require.ErrorIs(t, Forget(ctx, repo, userID, wtID), ErrNotForgettable)
	got, err := repo.GetWorktree(ctx, wtID)
	require.NoError(t, err)
	assert.False(t, got.CleanupMetadata.DirectoryDeleted)
	assert.False(t, got.CleanupMetadata.Forgotten)
}
