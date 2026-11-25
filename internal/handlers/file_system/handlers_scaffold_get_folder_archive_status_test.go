// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestGetFolderArchiveStatus_Generated: rows for `tdd.RunRPCCases`. Depth: `forge skill load testing/patterns`.
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
func TestGetFolderArchiveStatus_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))
	const archiveID = "0123456789abcdef0123456789abcdef"

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetFolderArchiveStatusRequest, pb.GetFolderArchiveStatusResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.GetFolderArchiveStatusRequest{ProjectId: "p-1", ArchiveId: archiveID}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "a malformed archive id is refused",
			Req:     connect.NewRequest(&pb.GetFolderArchiveStatusRequest{ProjectId: "p-1", ArchiveId: "abc/def"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.GetFolderArchiveStatusRequest{ProjectId: "p-missing", ArchiveId: archiveID}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetFolderArchiveStatus)
}
