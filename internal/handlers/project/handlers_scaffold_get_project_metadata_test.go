package project_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectMetadata_Generated(t *testing.T) {
	t.Parallel()

	svc := newService(t, ownedProjectRepo(t), nil)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetProjectMetadataRequest, pb.GetProjectMetadataResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     &pb.GetProjectMetadataRequest{ProjectId: "p-2"},
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the project's stored metadata is returned",
			Req:  &pb.GetProjectMetadataRequest{ProjectId: "p-1"},
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetProjectMetadataResponse) {
				m := resp
				if m.GetProjectId() != "p-1" || m.GetName() != "proj" || m.GetPath() != creatorPath || !m.GetIsGitRepo() || m.GetDefaultBranch() != "main" {
					t.Fatalf("metadata = %v, want p-1's", m)
				}
			},
		},
	}, svc.GetProjectMetadata)
}
