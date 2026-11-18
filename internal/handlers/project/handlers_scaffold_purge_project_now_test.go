// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// PurgeProjectNow's rows. The purge over a real database is in
// project_delete_test.go and internal/db/project_schedule_test.go.
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

func TestPurgeProjectNow_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var purged []string
	store.PurgeProjectNowFunc = func(ctx context.Context, id string, decided core.ProjectDecision, afterClaim func(context.Context)) ([]string, error) {
		if afterClaim != nil {
			afterClaim(ctx)
		}
		purged = append(purged, id+" by "+decided.OwnerUserID)
		return nil, nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PurgeProjectNowRequest, pb.PurgeProjectNowResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.PurgeProjectNowRequest{ProjectId: "p-2", ConfirmName: "proj"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without the project's name nothing is deleted",
			Req:     connect.NewRequest(&pb.PurgeProjectNowRequest{ProjectId: "p-1", ConfirmName: "pro"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "the owner naming it deletes it at once",
			Req:  connect.NewRequest(&pb.PurgeProjectNowRequest{ProjectId: "p-1", ConfirmName: "proj"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, _ *connect.Response[pb.PurgeProjectNowResponse]) {
				if len(purged) != 1 || purged[0] != "p-1 by test-user" {
					t.Fatalf("purged = %v, want p-1 by test-user", purged)
				}
			},
		},
	}, svc.PurgeProjectNow)
}
