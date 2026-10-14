package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestTouchProject_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var touched []string
	store.TouchProjectFunc = func(_ context.Context, id, userID string) error {
		touched = append(touched, id+" by "+userID)
		return nil
	}
	// Without a daemon router there is no MCP warmup to start.
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.TouchProjectRequest, pb.TouchProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.TouchProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's project is marked active",
			Req:  connect.NewRequest(&pb.TouchProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.TouchProjectResponse]) {
				if !resp.Msg.GetSuccess() || len(touched) != 1 || touched[0] != "p-1 by test-user" {
					t.Fatalf("success = %v, touched = %v, want p-1 by test-user", resp.Msg.GetSuccess(), touched)
				}
			},
		},
	}, svc.TouchProject)
}
