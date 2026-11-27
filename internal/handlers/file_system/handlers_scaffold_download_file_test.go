package file_system_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// The download served from the owning machine, its cap and its refusals are
// TestDownloadFile_* (rpc_download_file_test.go); these rows need no database.
func TestDownloadFile_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Placement: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DownloadFileRequest, pb.DownloadFileResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.DownloadFileRequest{ProjectId: "p-1", Path: "a.xlsx"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "path is required",
			Req:     connect.NewRequest(&pb.DownloadFileRequest{ProjectId: "p-1"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.DownloadFileRequest{Path: "a.xlsx"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.DownloadFileRequest{ProjectId: "p-missing", Path: "a.xlsx"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DownloadFile)
}
