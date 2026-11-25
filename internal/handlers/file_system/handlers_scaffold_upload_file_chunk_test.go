// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestUploadFileChunk_Generated: rows for `tdd.RunRPCCases`. Depth: `forge skill load testing/patterns`.
package file_system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// What a chunk sends the machine, its refusals and the legacy whole-file
// write are TestUploadFileChunk_* in upload_test.go, over a seeded project;
// these rows need no database.
func TestUploadFileChunk_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UploadFileChunkRequest, pb.UploadFileChunkResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.UploadFileChunkRequest{ProjectId: "p-1", UploadId: "ab", Data: []byte("x")}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.UploadFileChunkRequest{UploadId: "ab", Data: []byte("x")}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.UploadFileChunkRequest{ProjectId: "p-missing", UploadId: "ab", Data: []byte("x")}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.UploadFileChunk)
}
