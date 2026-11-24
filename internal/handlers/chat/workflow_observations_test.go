// Copyright (c) 2026 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// What a workflow's nodes recorded with observe: is in the FULL view only
// (the one `reliant workflow status` reads), under the workflow it ran in.
// A viewer of a shared chat never reads it: its attributes and events are
// derived from what the steps produced, which a viewer is not shown either.
func TestWorkflowExecutionsCarryObservationsToTheOwnerInTheFullViewOnly(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	step := f.SeedResolvedStep(t)
	wrote, err := repo.RecordNodeObservation(context.Background(), &db.NodeObservation{
		ID: "obs-1", ChatID: f.ChatID, WorkflowID: step.WorkflowID, RunID: "run-1", StepID: "plan", NodePath: "plan",
		Attempt: 1, Source: db.NodeObservationSourceWorkflow,
		Attributes: `{"trigger":"schedule"}`,
		Events:     `[{"name":"decision","attributes":{"name":"cut","value":"no_cut","reason":"main_unchanged"}}]`,
		Errors:     `[]`, CreatedAt: viewertest.SeededAt,
	}, 10)
	require.NoError(t, err)
	require.Equal(t, db.NodeObservationStored, wrote)

	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Orgs: f.Membership, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}))
	read := func(ctx context.Context, view reliantv1.WorkflowExecutionView) *reliantv1.WorkflowExecution {
		t.Helper()
		resp, err := svc.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{ChatId: f.ChatID, View: view}))
		require.NoError(t, err)
		for _, root := range resp.Msg.GetAllRootWorkflows() {
			if root.GetId() == step.WorkflowID {
				return root
			}
		}
		t.Fatalf("workflow %s is not in the tree", step.WorkflowID)
		return nil
	}

	owned := read(f.Owner, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL)
	require.Len(t, owned.GetObservations(), 1)
	got := owned.GetObservations()[0]
	assert.Equal(t, "plan", got.GetNodeId())
	assert.Equal(t, step.WorkflowID, got.GetWorkflowId())
	assert.Equal(t, "schedule", got.GetAttributes().AsMap()["trigger"])
	require.Len(t, got.GetEvents(), 1)
	assert.Equal(t, "decision", got.GetEvents()[0].GetName())
	assert.Equal(t, "no_cut", got.GetEvents()[0].GetAttributes().AsMap()["value"])

	assert.Empty(t, read(f.Owner, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC).GetObservations(),
		"the chat timeline's BASIC view stays as lean as it was")
	assert.Empty(t, read(f.Viewer, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_FULL).GetObservations(),
		"a viewer never reads what a node recorded")
}

// ElideWorkflowsForViewer drops observations at every depth of the tree, not
// only on the roots: the read above never fetches them for a viewer, and the
// elision is what holds for any other caller.
func TestElideWorkflowsForViewerDropsObservations(t *testing.T) {
	obs := func() []*reliantv1.NodeObservation {
		return []*reliantv1.NodeObservation{{NodeId: "plan"}}
	}
	tree := []*reliantv1.WorkflowExecution{{
		Observations: obs(),
		Children:     []*reliantv1.WorkflowExecution{{Observations: obs()}},
	}}
	chatproto.ElideWorkflowsForViewer(tree)
	assert.Empty(t, tree[0].GetObservations())
	assert.Empty(t, tree[0].GetChildren()[0].GetObservations())
}

// The read degrades: a failed observation read returns the tree without them.
func TestWorkflowExecutionsWithoutObservationsWhenTheirReadFails(t *testing.T) {
	svc := &Service{deps: Deps{Repo: failingObservationsRepo{}}}
	assert.Nil(t, svc.chatObservations(context.Background(), "chat"))
}

type failingObservationsRepo struct{ Repository }

func (failingObservationsRepo) ListChatNodeObservations(context.Context, string, int) ([]*db.NodeObservation, error) {
	return nil, assert.AnError
}
