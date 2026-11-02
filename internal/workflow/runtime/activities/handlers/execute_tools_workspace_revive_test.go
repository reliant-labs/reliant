// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Reclaim on merge archives a landed worktree under a chat that is still
// live, and its daemon removes the checkout. When that chat works again, its
// first tool batch brings the worktree back: the row is made live, the daemon
// retires the archive's fence and recreates the checkout from the branch, and
// the model is told what happened.

// archiveUnderTheChat archives the fixture's worktree the way the sweep does
// for a landed one, and records its checkout as removed. It returns the
// archive's fence.
func (f *workspaceRecoveryFixture) archiveUnderTheChat(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ok, err := f.h.Repo().ArchiveLandedWorktree(ctx, f.worktreeID, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, f.h.Repo().MergeWorktreeCleanupMetadata(ctx, f.worktreeID, func(m *db.CleanupMetadata, _ bool) bool {
		m.DirectoryDeleted = true
		return true
	}))
	wt, err := f.h.Repo().GetWorktree(ctx, f.worktreeID)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt)
	return strconv.FormatInt(wt.DeletedAt.UnixNano(), 10)
}

// recreatingDaemon recreates a missing workspace, as worktree.ensure does.
func recreatingDaemon(t *testing.T) *daemonLikeExecutor {
	executor := &daemonLikeExecutor{}
	executor.ensure = func(req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		if osutil.ValidateWorkingDir(req.Path) == nil {
			return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspacePresent, Path: req.Path}, nil
		}
		if !req.Repair {
			return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceFallback, Path: req.FallbackPath, Detail: "not recreatable"}, nil
		}
		require.NoError(t, os.MkdirAll(req.Path, 0o755))
		return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceRepaired, Path: req.Path, Notice: &toolexec.WorkspaceNotice{
			Kind: toolexec.NoticeRepaired, WorkspacePath: req.Path, RunningIn: req.Path, Branch: req.Branch, Checkouts: []string{""}, At: time.Now(),
		}}, nil
	}
	return executor
}

func TestExecuteTools_LiveChatGetsItsReclaimedWorktreeBack(t *testing.T) {
	t.Parallel()
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	fence := f.archiveUnderTheChat(t)
	executor := recreatingDaemon(t)

	out := f.run(t, executor, bashCall("git status"))
	require.Len(t, out.ToolResults, 1)
	content := out.ToolResults[0].GetContent()
	assert.False(t, out.ToolResults[0].GetIsError(), content)
	assert.Contains(t, content, "ran in "+f.workspace, "the call runs in the chat's own workspace, not the main checkout")
	assert.Contains(t, content, "[Workspace restored]")
	assert.Contains(t, content, "after its work landed")
	assert.Contains(t, content, "recreated from branch `test-branch`")
	assert.Contains(t, content, "install")

	require.Equal(t, 1, executor.ensureCount())
	req := executor.ensures[0]
	assert.True(t, req.Repair, "the revived worktree is recreated from its branch")
	assert.Equal(t, fence, req.Retire, "and the daemon retires the archive's fence first")
	assert.Equal(t, f.worktreeID, req.WorktreeID)

	wt, err := f.h.Repo().GetWorktree(context.Background(), f.worktreeID)
	require.NoError(t, err)
	assert.Nil(t, wt.DeletedAt, "the row is live again")
	require.NotNil(t, wt.CleanupMetadata)
	assert.Equal(t, fence, wt.CleanupMetadata.RetireFence, "the sweep delivers the retirement too, should this ensure not have")
	assert.False(t, wt.CleanupMetadata.DirectoryDeleted)

	again := f.run(t, executor, bashCall("ls"))
	assert.NotContains(t, again.ToolResults[0].GetContent(), "[Workspace restored]", "said once")
	assert.Equal(t, fence, executor.ensures[1].Retire, "retain retirement until the sweep persists positive acknowledgement")
}

func TestExecuteTools_RetirementDeferredAcrossBatchesRefusesUntilAck(t *testing.T) {
	t.Parallel()
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	fence := f.archiveUnderTheChat(t)
	executor := recreatingDaemon(t)
	recreate := executor.ensure
	executor.ensure = func(req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceBlocked}, nil
	}
	for i := 0; i < 2; i++ {
		out := f.run(t, executor, bashCall("pwd"))
		require.True(t, out.ToolResults[0].GetIsError())
		assert.Contains(t, out.ToolResults[0].GetContent(), "Retry")
		require.Empty(t, executor.requests, "deferred old remover must exclude tools even though row is live")
		assert.Equal(t, fence, executor.ensures[i].Retire)
	}
	wt, err := f.h.Repo().GetWorktree(context.Background(), f.worktreeID)
	require.NoError(t, err)
	require.Nil(t, wt.DeletedAt)
	require.Equal(t, fence, wt.CleanupMetadata.RetireFence)
	executor.ensure = recreate
	out := f.run(t, executor, bashCall("pwd"))
	require.False(t, out.ToolResults[0].GetIsError(), out.ToolResults[0].GetContent())
	require.Len(t, executor.requests, 1)
	assert.Equal(t, fence, executor.ensures[2].Retire)
}

func TestExecuteTools_WholeIdleRetirementRevivesWithDurableFence(t *testing.T) {
	t.Parallel()
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()
	ok, err := f.h.Repo().ArchiveRecoverableIdleWorktree(ctx, f.worktreeID, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	executor := recreatingDaemon(t)
	out := f.run(t, executor, bashCall("pwd"))
	require.False(t, out.ToolResults[0].GetIsError(), out.ToolResults[0].GetContent())
	require.NotEmpty(t, executor.ensures[0].Retire)
	f.run(t, executor, bashCall("ls"))
	require.Equal(t, executor.ensures[0].Retire, executor.ensures[1].Retire)
	wt, err := f.h.Repo().GetWorktree(ctx, f.worktreeID)
	require.NoError(t, err)
	require.Nil(t, wt.DeletedAt)
}

func TestExecuteTools_ArchivedWorktreeWithSavedWorkIsLeftForRecreate(t *testing.T) {
	t.Parallel()
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	f.archiveUnderTheChat(t)
	require.NoError(t, f.h.Repo().MergeWorktreeCleanupMetadata(context.Background(), f.worktreeID, func(m *db.CleanupMetadata, _ bool) bool {
		m.SnapshotRefs = []string{"refs/reliant/wip/" + f.worktreeID + "/root/1"}
		return true
	}))
	executor := recreatingDaemon(t)

	out := f.run(t, executor, bashCall("pwd"))
	require.True(t, out.ToolResults[0].GetIsError())
	assert.Contains(t, out.ToolResults[0].GetContent(), "preserved work")
	require.Zero(t, executor.ensureCount(), "saved work is restored by explicit recovery, never branch-only repair")
	require.Empty(t, executor.requests)
	wt, err := f.h.Repo().GetWorktree(context.Background(), f.worktreeID)
	require.NoError(t, err)
	assert.NotNil(t, wt.DeletedAt)
}

func TestExecuteTools_RevivalBlockedByANameRefusesSafely(t *testing.T) {
	t.Parallel()
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	f.archiveUnderTheChat(t)
	ctx := context.Background()
	wt, err := f.h.Repo().GetWorktree(ctx, f.worktreeID)
	require.NoError(t, err)
	holder := &db.Worktree{ID: uuid.NewString(), ProjectID: wt.ProjectID, Name: wt.Name, Path: t.TempDir(), Branch: "other"}
	require.NoError(t, f.h.Repo().CreateWorktree(ctx, holder))
	executor := recreatingDaemon(t)

	out := f.run(t, executor, bashCall("pwd"))
	assert.True(t, out.ToolResults[0].GetIsError(), out.ToolResults[0].GetContent())
	require.Zero(t, executor.ensureCount())
	require.Empty(t, executor.requests, "never run in an archived workspace or silently switch branches")
	wt, err = f.h.Repo().GetWorktree(ctx, f.worktreeID)
	require.NoError(t, err)
	assert.NotNil(t, wt.DeletedAt)
}
