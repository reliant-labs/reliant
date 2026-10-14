package project_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectInitStatus_Generated(t *testing.T) {
	t.Parallel()

	unrouted := newService(t, ownedProjectRepo(t), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectInitStatusRequest, pb.GetProjectInitStatusResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectInitStatusRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a project the daemon cannot check counts as initialized",
			Req:  connect.NewRequest(&pb.GetProjectInitStatusRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectInitStatusResponse]) {
				if !resp.Msg.GetInitialized() || resp.Msg.GetProjectId() != "p-1" {
					t.Fatalf("status = %v, want p-1 initialized", resp.Msg)
				}
			},
		},
	}, unrouted.GetProjectInitStatus)

	routed := newService(t, ownedProjectRepo(t), answering("project.check_init_status",
		`{"path":"/work","is_git_repo":true}`, `{"initialized":false,"message":"reliant.md is missing"}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectInitStatusRequest, pb.GetProjectInitStatusResponse]{
		{
			Name: "the daemon's check is reported",
			Req:  connect.NewRequest(&pb.GetProjectInitStatusRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectInitStatusResponse]) {
				if resp.Msg.GetInitialized() || resp.Msg.GetMessage() != "reliant.md is missing" {
					t.Fatalf("status = %v, want the daemon's not-initialized report", resp.Msg)
				}
			},
		},
	}, routed.GetProjectInitStatus)
}
