// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/planningtest"
	"github.com/reliant-labs/reliant/internal/store"
)

type toolRefetchCall struct {
	chatID string
	userID string
	kind   core.RefetchType
	opts   core.RefetchOpts
}

type toolRefetchRecorder struct{ calls []toolRefetchCall }

func (r *toolRefetchRecorder) EmitChatRefetch(_ context.Context, chatID string, kind core.RefetchType) error {
	r.calls = append(r.calls, toolRefetchCall{chatID: chatID, kind: kind})
	return nil
}

func (r *toolRefetchRecorder) EmitUserRefetch(_ context.Context, userID string, kind core.RefetchType, opts core.RefetchOpts) error {
	r.calls = append(r.calls, toolRefetchCall{userID: userID, kind: kind, opts: opts})
	return nil
}

// Execute factory-built tools, not their concrete constructors: losing the
// ToolsOptions.Refetch -> factory -> tool hop must fail these assertions.
func TestToolsFactory_PlanningAnnouncesThroughRefetch(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	chatID := uuid.NewString()
	createTestChat(t, repo, chatID)
	rc := createTestContext(t, chatID)
	refetch := &toolRefetchRecorder{}
	plans := planningtest.New(t, repo)
	factory := NewToolsFactory(&ToolsOptions{Repo: repo, Planning: plans, Refetch: refetch})

	resp := callRunTool(t, factory.CreatePlan(), rc, "create_plan", CreatePlanParams{
		Title: "Routing regression", Description: "Announce plan and task mutations", Tasks: []TaskInput{{Title: "First task"}},
	})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []toolRefetchCall{{chatID: chatID, kind: core.RefetchPlanTasks}}, refetch.calls)

	refetch.calls = nil
	resp = callRunTool(t, factory.UpdateTask(), rc, "update_task", UpdateTaskParams{TaskID: "1", Status: "completed"})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []toolRefetchCall{{chatID: chatID, kind: core.RefetchPlanTasks}}, refetch.calls)
}

func TestToolsFactory_WorktreeAnnouncesThroughRefetch(t *testing.T) {
	f := newWorktreeToolFixture(t)
	refetch := &toolRefetchRecorder{}
	factory := NewToolsFactory(&ToolsOptions{
		Repo: f.repo, Placement: f.repo, WorktreeChats: store.NewChatExtStore(f.repo.ORM()),
		RunMachine: f.machine, WorktreeSettler: f.settler, Refetch: refetch,
	})
	resp := callRunTool(t, factory.Worktree(), f.rc, WorktreeToolName, WorktreeParams{Action: "create", Name: "refetch-routing"})
	require.False(t, resp.IsError, resp.Content)
	row := f.row(t, "refetch-routing")
	require.NotNil(t, row)
	require.Equal(t, []toolRefetchCall{{
		userID: runTestUser, kind: core.RefetchWorktreeChanges,
		opts: core.RefetchOpts{ProjectID: &f.project.ID, WorktreeID: &row.ID},
	}}, refetch.calls)
}
