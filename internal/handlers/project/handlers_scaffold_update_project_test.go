package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdateProject_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var stored *db.Project
	store.UpdateProjectFunc = func(_ context.Context, p *db.Project, userID string) error {
		if userID != "test-user" {
			t.Errorf("UpdateProject as %q, want test-user", userID)
		}
		stored = p
		return nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateProjectRequest, pb.UpdateProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.UpdateProjectRequest{ProjectId: "p-2", Name: ptr.Of("renamed")}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "only the fields sent are changed",
			Req:  connect.NewRequest(&pb.UpdateProjectRequest{ProjectId: "p-1", Name: ptr.Of("renamed")}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateProjectResponse]) {
				if stored == nil || stored.Name != "renamed" || stored.Path != "/work" || stored.DefaultBranch == nil || *stored.DefaultBranch != "main" {
					t.Fatalf("stored = %+v, want p-1 renamed and otherwise unchanged", stored)
				}
				if got := resp.Msg.GetProject(); got.GetName() != "renamed" || got.GetId() != "p-1" {
					t.Fatalf("project = %v, want p-1 renamed", got)
				}
			},
		},
	}, svc.UpdateProject)
}
