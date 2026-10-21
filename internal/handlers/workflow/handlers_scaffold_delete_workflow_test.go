package workflow_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"

	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

func TestDeleteWorkflow_Generated(t *testing.T) {
	t.Parallel()

	slug := workflowref.Slug("My Flow")
	store := draftsRepo(callerDraft(t, slug))
	store.DeleteWorkflowDraftBySlugFunc = func(context.Context, string, string) error { return nil }
	store.ListTriggersFunc = func(context.Context, core.TriggerFilters) ([]*core.Trigger, error) { return nil, nil }
	store.RunTxFunc = func(ctx context.Context, f func(context.Context) error) error { return f(ctx) }
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: store}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteWorkflowRequest, pb.DeleteWorkflowResponse]{
		{
			Name:    "a project is required",
			Req:     connect.NewRequest(&pb.DeleteWorkflowRequest{Name: "My Flow"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a name is required",
			Req:     connect.NewRequest(&pb.DeleteWorkflowRequest{ProjectId: "p-1"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a workflow the caller does not have is not found",
			Req:     connect.NewRequest(&pb.DeleteWorkflowRequest{ProjectId: "p-1", Name: "Someone Else's"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's workflow is deleted by the slug of its name",
			Req:  connect.NewRequest(&pb.DeleteWorkflowRequest{ProjectId: "p-1", Name: "My Flow"}),
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeleteWorkflowResponse]) {
				if !resp.Msg.GetSuccess() {
					t.Fatalf("success = false, want true")
				}
				calls := store.Calls("DeleteWorkflowDraftBySlug")
				if len(calls) != 1 || calls[0].Args[1] != "test-user" || calls[0].Args[2] != slug {
					t.Fatalf("deletes = %v, want one of test-user's %q", calls, slug)
				}
			},
		},
	}, svc.DeleteWorkflow)
}
