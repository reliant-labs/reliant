package project_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestInitializeProject_Generated(t *testing.T) {
	t.Parallel()

	svc := newService(t, ownedProjectRepo(t), nil)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.InitializeProjectRequest, pb.InitializeProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     &pb.InitializeProjectRequest{ProjectId: "p-2"},
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a project is initialized when it is created, so this only confirms it",
			Req:  &pb.InitializeProjectRequest{ProjectId: "p-1"},
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.InitializeProjectResponse) {
				if !resp.GetInitialized() || resp.GetProjectId() != "p-1" || resp.GetStatus() != "initialized" {
					t.Fatalf("response = %v, want p-1 initialized", resp)
				}
			},
		},
	}, svc.InitializeProject)
}
