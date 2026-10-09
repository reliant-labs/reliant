// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const (
	noticeProject   = "/home/workspace/projects/reliant-labs"
	noticeWorkspace = "/home/workspace/.reliant/worktrees/reliant-labs/errors-5972f1d1"
)

// Fallback to the main checkout: the model is told it is no longer on its
// branch, in a checkout other chats share, and how the branch comes back.
func TestWorkspaceNoticeText_FallbackToMainCheckout(t *testing.T) {
	text := workspaceNoticeText(&toolexec.WorkspaceNotice{
		Kind: toolexec.NoticeFallback, WorkspacePath: noticeWorkspace, RunningIn: noticeProject,
		Branch: "fix/errors", Reason: "branch 'fix/errors' no longer exists in " + noticeProject + "/reliant",
	}, &db.Project{Path: noticeProject})

	assert.Contains(t, text, noticeWorkspace)
	assert.Contains(t, text, "no longer exists in "+noticeProject+"/reliant")
	assert.Contains(t, text, "Instead, this call ran in the project's main checkout, "+noticeProject)
	assert.Contains(t, text, "NOT on branch `fix/errors`")
	assert.Contains(t, text, "Recreate workspace")
	assert.Contains(t, text, "Move to main checkout")
	assert.Contains(t, text, "Until then every call runs in "+noticeProject+".")
}

// The prod incident: the project is gone too, and the call ran in $HOME. Do
// not send the model to a repository that does not exist.
func TestWorkspaceNoticeText_FallbackWhenTheProjectIsGoneToo(t *testing.T) {
	text := workspaceNoticeText(&toolexec.WorkspaceNotice{
		Kind: toolexec.NoticeFallback, WorkspacePath: noticeWorkspace, RunningIn: "/home/workspace",
		Branch: "fix/errors", Reason: "the project's repository " + noticeProject + "/forge is missing on this machine",
	}, &db.Project{Path: noticeProject})

	assert.Contains(t, text, "this call ran in /home/workspace, because the project's checkout "+noticeProject+" is missing too")
	assert.Contains(t, text, "cloned again to "+noticeProject)
	assert.NotContains(t, text, "fetch it in the project's repositories")
}

// A chat on the main checkout whose project directory is gone: there is no
// branch to talk about, and "missing too" would be wrong.
func TestWorkspaceNoticeText_MainCheckoutChatFallsBackToHome(t *testing.T) {
	text := workspaceNoticeText(&toolexec.WorkspaceNotice{
		Kind: toolexec.NoticeFallback, WorkspacePath: noticeProject, RunningIn: "/home/workspace",
		Reason: "it is not a worktree reliant created, and only those are recreated",
	}, &db.Project{Path: noticeProject})

	assert.Contains(t, text, "This chat's working directory "+noticeProject)
	assert.Contains(t, text, "Instead, this call ran in /home/workspace.")
	assert.NotContains(t, text, "missing too")
	assert.NotContains(t, text, "branch")
}

func TestWorkspaceNoticeText_RepairedNamesTheBranchAndWhatWasLost(t *testing.T) {
	text := workspaceNoticeText(&toolexec.WorkspaceNotice{
		Kind: toolexec.NoticeRepaired, WorkspacePath: noticeWorkspace, RunningIn: noticeWorkspace,
		Branch: "fix/errors", Checkouts: []string{"control-plane", "forge", "reliant"},
	}, &db.Project{Path: noticeProject})

	assert.Contains(t, text, "recreated from branch `fix/errors` (control-plane, forge, reliant)")
	assert.Contains(t, text, "Uncommitted changes")
}

func TestWorkspaceNoticeText_NothingToSay(t *testing.T) {
	assert.Empty(t, workspaceNoticeText(nil, &db.Project{Path: noticeProject}))
	assert.Empty(t, workspaceNoticeText(&toolexec.WorkspaceNotice{Kind: "something-new"}, nil))
}
