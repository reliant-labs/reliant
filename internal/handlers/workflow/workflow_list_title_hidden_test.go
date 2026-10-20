package workflow

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
	"google.golang.org/protobuf/proto"
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

	// A user's building block: `hidden: true` in its own definition.
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", Name: "shared-step", Slug: "shared-step",
		Definition: "name: shared-step\nhidden: true\napiVersion: v2\ninputs:\n  model:\n    type: model\nentry: [ask]\nnodes:\n  - id: ask\n    type: call_llm\n    args:\n      model: \"{{inputs.model}}\"\n",
		Status:     db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))

	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))
	list := func(includeHidden bool) map[string]*reliantv1.WorkflowListItem {
		resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
			ProjectId: projectID, IncludeHidden: proto.Bool(includeHidden),
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
	require.NotContains(t, def, "shared-step", "a user definition's hidden: true keeps it out of the default listing")
	// The Autopilot pack: its workflows only start from their triggers, and
	// its helpers are hidden building blocks as well.
	require.True(t, def["builtin://autopilot-pr-review"].AutomationOnly)
	require.NotContains(t, def, "builtin://autopilot-lease-acquire")
	require.NotContains(t, def, "builtin://autopilot-agent")

	all := list(true)
	require.Contains(t, all, "builtin://structured-agent")
	require.Equal(t, "Structured Agent", all["builtin://structured-agent"].Title)
	require.True(t, all["builtin://structured-agent"].IsHidden)
	require.True(t, all["builtin://scope-conversation"].IsHidden)
	require.False(t, all["builtin://agent"].IsHidden)
	require.Contains(t, all, "builtin://scope-conversation")
	require.True(t, all["shared-step"].IsHidden, "the management listing keeps it, marked hidden")
	for _, helper := range []string{"autopilot-agent", "autopilot-lease-acquire", "autopilot-lease-release", "autopilot-set-labels", "autopilot-ensure-labels"} {
		w := all["builtin://"+helper]
		require.NotNil(t, w, helper)
		require.True(t, w.IsHidden, "%s is a building block", helper)
		require.True(t, w.AutomationOnly, "%s never starts from a chat", helper)
	}
}
