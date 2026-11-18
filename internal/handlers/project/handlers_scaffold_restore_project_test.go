// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// RestoreProject's rows. The restore's rules over a real database (the
// authority table, the triggers it resumes) are in project_delete_test.go.
package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestRestoreProject_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var restored []string
	store.RestoreProjectFunc = func(_ context.Context, id string, _ core.ProjectDecision) ([]string, error) {
		restored = append(restored, id)
		return []string{"trigger-1"}, nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RestoreProjectRequest, pb.RestoreProjectResponse]{
		{
			Name:    "no project_id is refused",
			Req:     connect.NewRequest(&pb.RestoreProjectRequest{}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found, and not restored",
			Req:     connect.NewRequest(&pb.RestoreProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the owner restores it, and is told how many automations are on again",
			Req:  connect.NewRequest(&pb.RestoreProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.RestoreProjectResponse]) {
				if len(restored) != 1 || restored[0] != "p-1" {
					t.Fatalf("restored = %v, want p-1", restored)
				}
				if resp.Msg.GetTriggersResumed() != 1 || resp.Msg.GetProject().GetId() != "p-1" {
					t.Fatalf("response = %v", resp.Msg)
				}
			},
		},
	}, svc.RestoreProject)
}
