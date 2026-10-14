package workflow_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
)

func TestSetWorkflowVisibility_Generated(t *testing.T) {
	t.Parallel()

	mine := callerDraft(t, "mine")
	store := draftsRepo(mine)
	store.SetWorkflowDraftHiddenFunc = func(_ context.Context, _ string, hidden bool) (*db.WorkflowDraft, error) {
		d := *mine
		d.IsHidden = hidden
		return &d, nil
	}
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: store}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetWorkflowVisibilityRequest, pb.SetWorkflowVisibilityResponse]{
		{
			Name:    "a project is required",
			Req:     connect.NewRequest(&pb.SetWorkflowVisibilityRequest{Slug: "mine", IsHidden: true}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a slug is required",
			Req:     connect.NewRequest(&pb.SetWorkflowVisibilityRequest{ProjectId: "p-1", IsHidden: true}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a workflow the caller does not have is not found",
			Req:     connect.NewRequest(&pb.SetWorkflowVisibilityRequest{ProjectId: "p-1", Slug: "theirs", IsHidden: true}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's workflow is hidden",
			Req:  connect.NewRequest(&pb.SetWorkflowVisibilityRequest{ProjectId: "p-1", Slug: "mine", IsHidden: true}),
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SetWorkflowVisibilityResponse]) {
				if !resp.Msg.GetSuccess() || !resp.Msg.GetWorkflow().GetIsHidden() {
					t.Fatalf("response = %v, want the workflow hidden", resp.Msg)
				}
				calls := store.Calls("SetWorkflowDraftHidden")
				if len(calls) != 1 || calls[0].Args[1] != "wf-mine" || calls[0].Args[2] != true {
					t.Fatalf("SetWorkflowDraftHidden calls = %v, want one hiding wf-mine", calls)
				}
			},
		},
	}, svc.SetWorkflowVisibility)
}
