// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/approvalkind"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// ApprovalCreate stamps every approval it writes with the min_role
// internal/approvalkind decides for its source, so a resolve path never has
// to re-derive it from a row that no longer records the tool
// (chat-collaboration.md §2.4). Every row it writes carries a value; NULL is
// left only for rows an older worker wrote, which read back as owner.
func TestApprovalCreateActivity_StampsMinRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input ApprovalCreateInput
		want  approvalkind.Role
	}{
		{"an approval node", ApprovalCreateInput{Title: "Deploy to production?"}, approvalkind.Reply},
		{"the manual-mode tool gate", ApprovalCreateInput{Title: "Approve tool execution?"}, approvalkind.Reply},
		{"a trigger change", ApprovalCreateInput{Title: "Enable it?", ToolCallID: "call-1", ToolName: tools.ToolEnableTrigger}, approvalkind.Manage},
		{"a machine start", ApprovalCreateInput{Title: "Start it?", ToolCallID: "call-1", ToolName: tools.ToolManageMachine}, approvalkind.Owner},
		{"a connection proposal", ApprovalCreateInput{
			Title: "Connect Slack?", ToolCallID: "call-1", ToolName: tools.ToolProposeConnection,
			ConnectionProposal: &core.ConnectionProposal{IntegrationID: "slack"},
		}, approvalkind.Owner},
		{"a tool call from workflow code that sends no tool name", ApprovalCreateInput{Title: "Enable it?", ToolCallID: "call-1"}, approvalkind.Owner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()

			userID, projectID, chatID, workflowID := uuid.New().String(), uuid.New().String(), uuid.New().String(), uuid.New().String()
			h.CreateTestProject(ctx, projectID, userID)
			h.CreateTestChat(ctx, chatID, projectID, userID)
			h.CreateTestWorkflow(ctx, workflowID, chatID)

			input := tc.input
			input.ChatID, input.WorkflowID, input.TemporalWorkflowID, input.StepID = chatID, workflowID, workflowID, "agent_loop"
			var out ApprovalCreateOutput
			require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(approvalCreateDeps(h.Repo())).Execute, input, &out))

			stored, err := h.Repo().GetApproval(ctx, out.ApprovalID)
			require.NoError(t, err)
			require.NotNil(t, stored.MinRole, "ApprovalCreate must stamp min_role")
			require.Equal(t, tc.want.String(), *stored.MinRole)
			require.Equal(t, tc.want, approvalkind.Stored(stored.MinRole))
			require.Nil(t, stored.DecidedBy, "nothing is decided at creation")
		})
	}
}
