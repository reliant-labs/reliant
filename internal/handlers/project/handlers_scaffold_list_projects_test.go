package project_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestListProjects_Generated(t *testing.T) {
	t.Parallel()

	var filters db.ProjectFilters
	store := &project.MockRepository{
		ListProjectsFunc: func(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
			filters = f
			return []*db.Project{ownedProject()}, nil
		},
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListProjectsRequest, pb.ListProjectsResponse]{
		{
			Name: "the caller's projects are listed, a page of 100 by default",
			Req:  connect.NewRequest(&pb.ListProjectsRequest{Offset: -3}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListProjectsResponse]) {
				if filters.UserID != "test-user" || filters.Limit != 100 || filters.Offset != 0 {
					t.Fatalf("filters = %+v, want test-user's first 100", filters)
				}
				if got := resp.Msg.GetProjects(); len(got) != 1 || got[0].GetId() != "p-1" || resp.Msg.GetTotal() != 1 {
					t.Fatalf("projects = %v (total %d), want p-1", got, resp.Msg.GetTotal())
				}
			},
		},
		{
			Name: "a page is the caller's to choose",
			Req:  connect.NewRequest(&pb.ListProjectsRequest{Limit: 5, Offset: 10}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, _ *connect.Response[pb.ListProjectsResponse]) {
				if filters.Limit != 5 || filters.Offset != 10 {
					t.Fatalf("filters = %+v, want limit 5 offset 10", filters)
				}
			},
		},
	}, svc.ListProjects)

	failing := newService(t, &project.MockRepository{
		ListProjectsFunc: func(context.Context, db.ProjectFilters) ([]*db.Project, error) {
			return nil, errors.New("connection reset")
		},
	}, nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListProjectsRequest, pb.ListProjectsResponse]{
		{
			Name:    "a store failure is internal",
			Req:     connect.NewRequest(&pb.ListProjectsRequest{}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInternal,
		},
	}, failing.ListProjects)
}
