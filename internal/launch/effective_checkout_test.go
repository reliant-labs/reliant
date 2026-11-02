// Copyright (c) 2025 Reliant Labs
package launch

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// A run's working directory is the project's checkout ON THE CHAT'S MACHINE:
// the project was created on a laptop, the chat runs on a cloud machine, and
// workflow.path must be the cloud machine's path, never the laptop's.
func TestGetEffectiveCheckout_IsTheChatMachinesOwnCheckout(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	launcher := NewLauncher(repo, threads.NewService(repo), nil, nil, "")
	project, err := repo.GetProject(ctx, chat.ProjectID)
	require.NoError(t, err)

	const cloud, cloudPath = "cloud-box", "/home/workspace/projects/greenfield"
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: "laptop", UserID: chat.UserID}))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, project.ID, "laptop", project.Path, nil))
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: cloud, UserID: chat.UserID}))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, project.ID, cloud, cloudPath, nil))
	pinned := cloud
	chat.ActiveDaemonID = &pinned

	assert.Equal(t, Checkout{Path: cloudPath}, launcher.GetEffectiveCheckout(ctx, chat))

	// The main worktree row mirrors projects.path: it is the main checkout on
	// the chat's machine too, not a branch workspace at the laptop's path.
	now := time.Now().UTC()
	mainRow := &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: project.Path, Branch: "main", BaseBranch: "main", ProjectID: project.ID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateWorktree(ctx, mainRow))
	chat.WorktreeID = &mainRow.ID
	assert.Equal(t, Checkout{Path: cloudPath}, launcher.GetEffectiveCheckout(ctx, chat))
}

// A chat on a machine with no checkout gets no path rather than a wrong one:
// the run starts and its first machine call is refused with the way out.
func TestGetEffectiveCheckout_NoCheckoutOnTheChatsMachineIsEmptyNotTheCreationPath(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	launcher := NewLauncher(repo, threads.NewService(repo), nil, nil, "")
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: "bare-box", UserID: chat.UserID}))
	pinned := "bare-box"
	chat.ActiveDaemonID = &pinned

	assert.Equal(t, Checkout{}, launcher.GetEffectiveCheckout(ctx, chat))
}
