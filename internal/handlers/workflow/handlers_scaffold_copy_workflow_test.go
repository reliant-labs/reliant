package workflow_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
)

func TestCopyWorkflow_Generated(t *testing.T) {
	t.Parallel()

	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: draftsRepo()}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CopyWorkflowRequest, pb.CopyWorkflowResponse]{
		{
			Name:    "a project is required",
			Req:     connect.NewRequest(&pb.CopyWorkflowRequest{SourceSlug: "builtin://agent"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a source is required",
			Req:     connect.NewRequest(&pb.CopyWorkflowRequest{ProjectId: "p-1"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.CopyWorkflowRequest{ProjectId: "p-2", SourceSlug: "deploy"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.CopyWorkflow)
}
