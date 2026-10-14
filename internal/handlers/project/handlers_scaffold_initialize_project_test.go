package project_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestInitializeProject_Generated(t *testing.T) {
	t.Parallel()

	svc := newService(t, ownedProjectRepo(t), nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.InitializeProjectRequest, pb.InitializeProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.InitializeProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a project is initialized when it is created, so this only confirms it",
			Req:  connect.NewRequest(&pb.InitializeProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.InitializeProjectResponse]) {
				if !resp.Msg.GetInitialized() || resp.Msg.GetProjectId() != "p-1" || resp.Msg.GetStatus() != "initialized" {
					t.Fatalf("response = %v, want p-1 initialized", resp.Msg)
				}
			},
		},
	}, svc.InitializeProject)
}
