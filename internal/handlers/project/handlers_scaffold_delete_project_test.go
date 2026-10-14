package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestDeleteProject_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var deleted []string
	store.DeleteProjectFunc = func(_ context.Context, id, userID string) error {
		deleted = append(deleted, id+" by "+userID)
		return nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteProjectRequest, pb.DeleteProjectResponse]{
		{
			Name:    "a project the caller does not own is not found, and not deleted",
			Req:     connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's project is deleted",
			Req:  connect.NewRequest(&pb.DeleteProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeleteProjectResponse]) {
				if !resp.Msg.GetSuccess() {
					t.Fatal("success = false")
				}
				if len(deleted) != 1 || deleted[0] != "p-1 by test-user" {
					t.Fatalf("deleted = %v, want only p-1 by test-user", deleted)
				}
			},
		},
	}, svc.DeleteProject)
}
