// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

type authorRows struct {
	root  *core.Workflow
	chat  *core.Chat
	wt    *core.Worktree
	repos []*core.Repo
}

func (a authorRows) GetWorkflow(context.Context, string) (*core.Workflow, error) { return a.root, nil }
func (a authorRows) GetChat(context.Context, string) (*core.Chat, error)         { return a.chat, nil }
func (a authorRows) GetWorktree(context.Context, string) (*core.Worktree, error) { return a.wt, nil }

func (a authorRows) ListReposByProject(context.Context, string) ([]*core.Repo, error) {
	return a.repos, nil
}

type executionFake struct {
	calls   int
	id, run string
	status  enumspb.WorkflowExecutionStatus
	err     error
}

func (e *executionFake) DescribeWorkflowExecution(_ context.Context, id, run string) (*client.WorkflowExecutionDescription, error) {
	e.calls++
	e.id, e.run = id, run
	if e.err != nil {
		return nil, e.err
	}
	return &client.WorkflowExecutionDescription{WorkflowExecutionMetadata: client.WorkflowExecutionMetadata{WorkflowExecution: workflow.Execution{ID: id, RunID: run}, Status: e.status}}, nil
}

func TestCheckoutUsesRegisteredNestedRepository(t *testing.T) {
	wtID := "wt"
	remote := "git@github.com:org/reliant.git"
	sibling := "https://github.com/org/forge.git"
	rows := authorRows{chat: &core.Chat{ID: "chat", UserID: "owner", ProjectID: "project", WorktreeID: &wtID}, wt: &core.Worktree{ID: wtID, ProjectID: "project", Path: "/workspaces/multi"}, repos: []*core.Repo{{ProjectID: "project", RelativePath: "reliant", RemoteURL: &remote}, {ProjectID: "project", RelativePath: "forge", RemoteURL: &sibling}}}
	adapter := Authors{Store: rows}
	author := prhandoff.Author{OwnerID: "owner", ChatID: "chat", WorktreeID: wtID}
	path, err := adapter.Checkout(context.Background(), author, prhandoff.Repository{ID: 42, Name: "org/reliant"})
	require.NoError(t, err)
	require.Equal(t, "/workspaces/multi/reliant", path)
	path, err = adapter.Checkout(context.Background(), author, prhandoff.Repository{ID: 43, Name: "org/forge"})
	require.NoError(t, err)
	require.Equal(t, "/workspaces/multi/forge", path)
	// Stable remote selection treats SSH/HTTPS as the same registered repo,
	// but a different GitHub repository cannot borrow its checkout.
	https := "https://github.com/org/reliant.git"
	rows.repos[0].RemoteURL = &https
	path, err = adapter.Checkout(context.Background(), author, prhandoff.Repository{ID: 42, Name: "org/reliant"})
	require.NoError(t, err)
	require.Equal(t, "/workspaces/multi/reliant", path)
	_, err = adapter.Checkout(context.Background(), author, prhandoff.Repository{ID: 99, Name: "attacker/reliant"})
	require.Error(t, err)
	rows.repos[0].RelativePath = "../escape"
	_, err = adapter.Checkout(context.Background(), author, prhandoff.Repository{ID: 42, Name: "org/reliant"})
	require.Error(t, err)
}

func TestAuthorAvailabilityNeverSubstitutesCompletedOrCancelledRun(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    core.WorkflowStatus
		temporal  enumspb.WorkflowExecutionStatus
		err       error
		available bool
	}{{"live", core.Active(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil, true}, {"paused", core.Paused(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil, true}, {"completed", core.Completed(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil, false}, {"cancelled", core.Cancelled(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil, false}, {"stale DB active", core.Active(), enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, nil, false}, {"gone", core.Active(), 0, serviceerror.NewNotFound("gone"), false}} {
		t.Run(tt.name, func(t *testing.T) {
			rootID, wtID := "root", "wt"
			rows := authorRows{root: &core.Workflow{ID: rootID, ChatID: "chat", Status: tt.status}, chat: &core.Chat{ID: "chat", UserID: "owner", WorkflowID: &rootID, WorktreeID: &wtID}}
			exec := &executionFake{status: tt.temporal, err: tt.err}
			a := Authors{Store: rows, Temporal: exec}
			got, err := a.Available(context.Background(), prhandoff.Author{OwnerID: "owner", RootID: "root", ExecutionID: "original-execution", ChatID: "chat", WorktreeID: "wt"})
			require.NoError(t, err)
			require.Equal(t, tt.available, got)
			if exec.calls > 0 {
				require.Equal(t, "root", exec.id)
				require.Equal(t, "original-execution", exec.run)
			}
		})
	}
}
