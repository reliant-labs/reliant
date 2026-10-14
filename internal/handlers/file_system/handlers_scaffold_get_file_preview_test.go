package file_system_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	file_system "github.com/reliant-labs/reliant/internal/handlers/file_system"
)

// previewNoProjectRepo knows no project. The harness's stub would answer a
// lookup with (nil, nil), which is not what a missing row looks like.
type previewNoProjectRepo struct{ file_system.Repository }

func (previewNoProjectRepo) GetProject(context.Context, string) (*db.Project, error) {
	return nil, core.ErrProjectNotFound
}

// The preview served from the owning machine, its refusals and its wake are
// TestFileSystemProxy_GetFilePreview_* (internal/handlers/worktree/machine_wake_test.go, next to the
// worktree fixtures they share); these rows need no database.
func TestGetFilePreview_Generated(t *testing.T) {
	t.Parallel()

	svc := file_system.NewTestFileSystem(t, file_system.WithFileSystemDeps(file_system.Deps{Repo: previewNoProjectRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetFilePreviewRequest, pb.GetFilePreviewResponse]{
		{
			Name:    "anonymous caller is unauthenticated",
			Req:     connect.NewRequest(&pb.GetFilePreviewRequest{ProjectId: "p-1", Path: "a.png"}),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "path is required",
			Req:     connect.NewRequest(&pb.GetFilePreviewRequest{ProjectId: "p-1"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.GetFilePreviewRequest{Path: "a.png"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown project is not found",
			Req:     connect.NewRequest(&pb.GetFilePreviewRequest{ProjectId: "p-missing", Path: "a.png"}),
			Ctx:     file_system.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetFilePreview)
}
