package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdateProjectMetadata_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var stored *db.Project
	store.UpdateProjectFunc = func(_ context.Context, p *db.Project, _ string) error {
		stored = p
		return nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.UpdateProjectMetadataRequest, pb.UpdateProjectMetadataResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     &pb.UpdateProjectMetadataRequest{ProjectId: "p-2", Description: ptr.Of("d")},
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the description is stored",
			Req:  &pb.UpdateProjectMetadataRequest{ProjectId: "p-1", Description: ptr.Of("the widgets service")},
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.UpdateProjectMetadataResponse) {
				if stored == nil || stored.Description == nil || *stored.Description != "the widgets service" || stored.Name != "proj" {
					t.Fatalf("stored = %+v, want p-1 with the new description", stored)
				}
				if got := resp.GetProject().GetDescription(); got != "the widgets service" {
					t.Fatalf("description = %q", got)
				}
			},
		},
	}, svc.UpdateProjectMetadata)
}
