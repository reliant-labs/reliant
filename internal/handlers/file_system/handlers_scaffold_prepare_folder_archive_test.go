// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestPrepareFolderArchive_Generated: rows for `tdd.RunRPCCases`. Depth: `forge skill load testing/patterns`.
package file_system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// The behaviour over a seeded project is in transfer_test.go; these rows
// need no database.
func TestPrepareFolderArchive_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PrepareFolderArchiveRequest, pb.PrepareFolderArchiveResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.PrepareFolderArchiveRequest{ProjectId: "p-1", Path: "src"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.PrepareFolderArchiveRequest{Path: "src"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.PrepareFolderArchiveRequest{ProjectId: "p-missing", Path: "src"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.PrepareFolderArchive)
}
