// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestReadFileRange_Generated: rows for `tdd.RunRPCCases`. Depth: `forge skill load testing/patterns`.
package file_system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// The behaviour over a seeded project is in transfer_test.go; these rows
// need no database.
func TestReadFileRange_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ReadFileRangeRequest, pb.ReadFileRangeResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: "p-1", Path: "a.bin"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "path or archive_id is required",
			Req:     connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: "p-1"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a malformed archive id is refused",
			Req:     connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: "p-1", ArchiveId: proto.String("../../etc/passwd")}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: "p-missing", Path: "a.bin"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ReadFileRange)
}
