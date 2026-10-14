package workflow_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
)

func TestExportWorkflow_Generated(t *testing.T) {
	t.Parallel()

	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: draftsRepo(callerDraft(t, "mine"))}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ExportWorkflowRequest, pb.ExportWorkflowResponse]{
		{
			Name:    "a project is required",
			Req:     connect.NewRequest(&pb.ExportWorkflowRequest{Slug: "mine"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a slug is required",
			Req:     connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a workflow the caller does not have is not found",
			Req:     connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "theirs"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an unknown builtin is not found",
			Req:     connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "builtin://no-such-workflow"}),
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a builtin is exported from the embedded set",
			Req:  connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "builtin://agent"}),
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ExportWorkflowResponse]) {
				if resp.Msg.GetFilename() != "agent.yaml" || len(resp.Msg.GetYamlContent()) == 0 || resp.Msg.GetWorkflow().GetName() != "agent" {
					t.Fatalf("export = %q (%d bytes, workflow %q), want agent.yaml", resp.Msg.GetFilename(), len(resp.Msg.GetYamlContent()), resp.Msg.GetWorkflow().GetName())
				}
			},
		},
		{
			Name: "the caller's workflow is exported under its slug",
			Req:  connect.NewRequest(&pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "mine"}),
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ExportWorkflowResponse]) {
				if resp.Msg.GetFilename() != "mine.yaml" || len(resp.Msg.GetYamlContent()) == 0 {
					t.Fatalf("export = %q (%d bytes), want mine.yaml", resp.Msg.GetFilename(), len(resp.Msg.GetYamlContent()))
				}
			},
		},
	}, svc.ExportWorkflow)
}
