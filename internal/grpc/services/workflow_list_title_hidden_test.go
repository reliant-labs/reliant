package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func TestWorkflowService_ListWorkflows_TitleAndHiddenBuiltins(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-title-hidden-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Title Hidden", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", Name: "Titled User Workflow", Slug: "titled-user-workflow",
		Definition: "name: titled-user-workflow\ntitle: My Titled Workflow\napiVersion: v2\ninputs:\n  model:\n    type: model\nentry: [ask]\nnodes:\n  - id: ask\n    type: call_llm\n    args:\n      model: \"{{inputs.model}}\"\n",
		Status:     db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))

	svc := &WorkflowService{database: repo}
	list := func(includeHidden bool) map[string]*reliantv1.WorkflowListItem {
		resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
			ProjectId: projectID, IncludeHidden: includeHidden,
		}))
		require.NoError(t, err)
		out := map[string]*reliantv1.WorkflowListItem{}
		for _, w := range resp.Msg.Workflows {
			out[w.Name] = w
		}
		return out
	}

	def := list(false)
	require.NotContains(t, def, "builtin://structured-agent")
	require.NotContains(t, def, "builtin://scope-conversation")
	require.Contains(t, def, "builtin://agent")
	require.Equal(t, "Agent", def["builtin://agent"].Title)
	require.Equal(t, "Build an App", def["builtin://forge-one-shot"].Title)
	require.Equal(t, "My Titled Workflow", def["Titled User Workflow"].Title)

	all := list(true)
	require.Contains(t, all, "builtin://structured-agent")
	require.Equal(t, "Structured Agent", all["builtin://structured-agent"].Title)
	require.True(t, all["builtin://structured-agent"].IsHidden)
	require.True(t, all["builtin://scope-conversation"].IsHidden)
	require.False(t, all["builtin://agent"].IsHidden)
	require.Contains(t, all, "builtin://scope-conversation")
}
