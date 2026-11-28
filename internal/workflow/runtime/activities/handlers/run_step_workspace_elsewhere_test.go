// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A run node whose `daemon:` selector names another machine than its chat's
// branch workspace lives on still runs there (a workflow that works today
// keeps working), but a failure says why: the command's directory is the
// workspace's, on a machine that does not hold it. Success keeps its output
// untouched, and a work_dir outside the workspace is no conflict.
func TestRunStep_SelectorAwayFromTheBranchWorkspaceExplainsTheFailure(t *testing.T) {
	t.Parallel()
	f := newChatCheckoutFixture(t)
	f.machine(t, "owner-box", t.TempDir())
	f.machine(t, "other-box", t.TempDir())
	ctx := context.Background()
	owner := "owner-box"
	now := time.Now().UTC()
	wt := &db.Worktree{
		ID: "wt-" + uuid.NewString(), Name: "feature", Path: t.TempDir(),
		Branch: "feature", BaseBranch: "main", ProjectID: f.project, ChatID: &f.chat, DaemonID: &owner,
		Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.h.Repo().CreateWorktree(ctx, wt))
	chat, err := store.GetChat(ctx, f.h.Repo().ORM(), f.chat)
	require.NoError(t, err)
	chat.WorktreeID = &wt.ID
	require.NoError(t, f.h.Repo().UpdateChat(ctx, chat))

	run := func(t *testing.T, selector string, workDir string, resp mockRunResponse) ExecuteRunStepOutput {
		t.Helper()
		executor := &mockRunExecutor{responses: []mockRunResponse{resp}}
		act := NewExecuteRunStepActivity(f.h.Repo(), workspaceRowsOf(f.h.Repo()), nil, executor)
		env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
		env.RegisterActivity(act.Execute)
		val, err := env.ExecuteActivity(act.Execute, ExecuteRunStepInput{
			WorkflowID: "wf-1", ChatID: f.chat, StepID: "build", Command: "make", WorkDir: workDir,
			DaemonSelector: &activitytypes.DaemonSelector{ID: selector},
		})
		require.NoError(t, err)
		require.Len(t, executor.calls, 1, "the command still runs")
		var out ExecuteRunStepOutput
		require.NoError(t, val.Get(&out))
		return out
	}
	failed := mockRunResponse{stderr: "working directory does not exist: " + wt.Path, exitCode: 1}

	out := run(t, "other-box", "", failed)
	assert.Contains(t, out.Stderr, "[Workspace elsewhere]")
	assert.Contains(t, out.Stderr, "lives on self-hosted machine owner-bo")
	assert.Contains(t, out.Stderr, "ran on self-hosted machine other-bo")
	assert.Contains(t, out.Stderr, "working directory does not exist", "the command's own error follows the note")
	assert.Contains(t, out.Output, "[Workspace elsewhere]")

	ok := run(t, "other-box", "", mockRunResponse{stdout: "built", stderr: "warn"})
	assert.Equal(t, "warn", ok.Stderr, "a command that succeeded keeps its output byte for byte")

	onOwner := run(t, "owner-box", "", failed)
	assert.NotContains(t, onOwner.Stderr, "[Workspace elsewhere]", "the selector picked the workspace's own machine")

	elsewhere := run(t, "other-box", "/srv/deploy", failed)
	assert.NotContains(t, elsewhere.Stderr, "[Workspace elsewhere]", "an absolute work_dir outside the workspace is no conflict")
}
