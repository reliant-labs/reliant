package workflow_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
)

func TestExportWorkflow_Generated(t *testing.T) {
	t.Parallel()

	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: draftsRepo(callerDraft(t, "mine"))}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ExportWorkflowRequest, pb.ExportWorkflowResponse]{
		{
			Name:    "a project is required",
			Req:     &pb.ExportWorkflowRequest{Slug: "mine"},
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a slug is required",
			Req:     &pb.ExportWorkflowRequest{ProjectId: "p-1"},
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a workflow the caller does not have is not found",
			Req:     &pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "theirs"},
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an unknown builtin is not found",
			Req:     &pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "builtin://no-such-workflow"},
			Ctx:     workflow.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a builtin is exported from the embedded set",
			Req:  &pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "builtin://agent"},
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ExportWorkflowResponse) {
				if resp.GetFilename() != "agent.yaml" || len(resp.GetYamlContent()) == 0 || resp.GetWorkflow().GetName() != "agent" {
					t.Fatalf("export = %q (%d bytes, workflow %q), want agent.yaml", resp.GetFilename(), len(resp.GetYamlContent()), resp.GetWorkflow().GetName())
				}
			},
		},
		{
			Name: "the caller's workflow is exported under its slug",
			Req:  &pb.ExportWorkflowRequest{ProjectId: "p-1", Slug: "mine"},
			Ctx:  workflow.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ExportWorkflowResponse) {
				if resp.GetFilename() != "mine.yaml" || len(resp.GetYamlContent()) == 0 {
					t.Fatalf("export = %q (%d bytes), want mine.yaml", resp.GetFilename(), len(resp.GetYamlContent()))
				}
			},
		},
	}, svc.ExportWorkflow)
}
