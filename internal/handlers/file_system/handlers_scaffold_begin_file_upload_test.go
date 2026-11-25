// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestBeginFileUpload_Generated: rows for `tdd.RunRPCCases`. Depth: `forge skill load testing/patterns`.
package file_system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// The session the machine opens, the name and place refusals, the size cap
// and the legacy fallback are TestBeginFileUpload_* in upload_test.go, over a
// seeded project; these rows need no database.
func TestBeginFileUpload_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.BeginFileUploadRequest, pb.BeginFileUploadResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.BeginFileUploadRequest{ProjectId: "p-1", Filename: "a.txt", Size: 1}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.BeginFileUploadRequest{Filename: "a.txt", Size: 1}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.BeginFileUploadRequest{ProjectId: "p-missing", Filename: "a.txt", Size: 1}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.BeginFileUpload)
}
