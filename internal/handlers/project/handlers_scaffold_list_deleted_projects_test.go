// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// ListDeletedProjects' rows. An admin's view of a departed member's project
// is in project_delete_test.go.
package project_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestListDeletedProjects_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	after := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	by := "test-user"
	store.ListDeletedProjectsFunc = func(_ context.Context, userID string, _ *core.OrgScope) ([]*core.Project, error) {
		mine := ownedProject()
		mine.DeleteAfter, mine.DeletedBy = &after, &by
		other := &db.Project{ID: "p-9", UserID: "someone-else", Name: "theirs", DeleteAfter: &after, DeletedBy: &userID}
		return []*core.Project{mine, other}, nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListDeletedProjectsRequest, pb.ListDeletedProjectsResponse]{
		{
			Name: "lists the caller's restorable projects with their purge date, and nothing they may not restore",
			Req:  connect.NewRequest(&pb.ListDeletedProjectsRequest{}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListDeletedProjectsResponse]) {
				got := resp.Msg.GetProjects()
				if len(got) != 1 || got[0].GetId() != "p-1" {
					t.Fatalf("projects = %v, want only p-1", got)
				}
				if got[0].GetDeleteAfter() != "2026-11-25T10:00:00Z" || got[0].GetDeletedByUserId() != "test-user" {
					t.Fatalf("p-1 = %v", got[0])
				}
			},
		},
	}, svc.ListDeletedProjects)
}
