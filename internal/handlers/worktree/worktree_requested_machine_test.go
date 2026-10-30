// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A workspace made from the new-chat composer goes on the machine its "Runs
// on" names, so the chat that follows runs where its checkout is.

func (f *placementFixture) ownedDaemon(t *testing.T, userID string) string {
	t.Helper()
	id := "daemon-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: id, UserID: userID}))
	return id
}

func TestCreateWorktree_PlacesOnTheRequestedMachine(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{DaemonId: &machine})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, machine)
}

func TestCreateWorktree_RefusesARequestedMachineThatIsNotTheCallers(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	foreign := f.ownedDaemon(t, uuid.NewString())

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{DaemonId: &foreign})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"), "nothing may be created on another user's machine")
}

// A copy of a workspace is made where the workspace is; naming another
// machine contradicts that.
func TestCreateWorktree_RefusesARequestedMachineOtherThanTheSources(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	source := f.worktree(t, "daemon-b")

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{SourceWorktreeId: &source.ID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"))
}

func TestCreateWorktree_RefusesARequestedMachineOtherThanTheChats(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	chatID := f.chat(t, f.userID, "daemon-b", "")

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// The requested-machine path keeps the placement path's project binding: a
// chat of another project of the caller's cannot own this project's workspace.
func TestCreateWorktree_RefusesARequestedMachineWithAnotherProjectsChat(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	now := time.Now().UTC()
	otherProject := uuid.NewString()
	require.NoError(t, f.repo.CreateProject(context.Background(), &db.Project{
		ID: otherProject, UserID: f.userID, Name: "Other", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	c := &db.Chat{ID: uuid.NewString(), UserID: f.userID, Title: "elsewhere", ProjectID: otherProject, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, f.repo.CreateChat(context.Background(), c))

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &c.ID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"))
}

// The row carries its machine to the web, so the composer can tell which
// workspaces a machine holds.
func TestWorktreeProto_CarriesItsMachine(t *testing.T) {
	t.Parallel()

	owner := "daemon-b"
	assert.Equal(t, owner, worktreeToProto(&db.Worktree{ID: "w", DaemonID: &owner}).GetDaemonId())
	assert.Nil(t, worktreeToProto(&db.Worktree{ID: "main", IsMain: true}).DaemonId)
}

// A chat pinned to A whose branch workspace is B's (a pin from before StartChat
// refused the combination). The chat's tools run on B, so branching it to a
// new workspace (copying files from it) is made on B, where its files are,
// and the copy is read there; naming B is accepted and naming A is refused.
func TestCreateWorktree_AChatPinnedOffItsWorkspaceBranchesOnTheOwner(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machineA := f.ownedDaemon(t, f.userID)
	machineB := f.ownedDaemon(t, f.userID)
	source := f.worktree(t, machineB)
	chatID := f.chat(t, f.userID, machineA, source.ID)

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{
		ChatId: &chatID, SourceWorktreeId: &source.ID, CopyFiles: []string{".env"},
	})
	require.NoError(t, err)
	assertPlacedOn(t, f.router, wt, machineB)
	assert.Equal(t, []string{machineB}, f.router.daemonsFor("worktree.copy_paths"), "the files are copied where they are")

	_, err = f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID, SourceWorktreeId: &source.ID, DaemonId: &machineB})
	require.NoError(t, err, "the workspace's machine is the chat's machine")
	_, err = f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID, DaemonId: &machineA})
	require.Error(t, err, "the stale pin is not the chat's machine")
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}
