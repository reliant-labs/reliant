package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectChanges_Generated(t *testing.T) {
	t.Parallel()

	routed := newService(t, ownedProjectRepo(t), answering("project.git_changes", `{"path":"/work"}`,
		`{"branch":"main","files":[{"path":"a.go","status":"modified"},{"path":"b.go","status":"untracked","is_new":true}],"total_files":2}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectChangesRequest, pb.GetProjectChangesResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectChangesRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the daemon's changes are returned with their status",
			Req:  connect.NewRequest(&pb.GetProjectChangesRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectChangesResponse]) {
				files := resp.Msg.GetFiles()
				if resp.Msg.GetBranch() != "main" || resp.Msg.GetTotalFiles() != 2 || len(files) != 2 {
					t.Fatalf("changes = %v, want two files on main", resp.Msg)
				}
				if files[0].GetStatus() != pb.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED ||
					files[1].GetStatus() != pb.FileChangeStatus_FILE_CHANGE_STATUS_UNTRACKED || !files[1].GetIsNew() {
					t.Fatalf("files = %v, want a.go modified and b.go new and untracked", files)
				}
			},
		},
	}, routed.GetProjectChanges)

	// Two nested repos and no repo_id: which one is meant is the caller's
	// to say.
	multi := ownedProjectRepo(t)
	multi.ListReposByProjectFunc = func(_ context.Context, projectID string) ([]*core.Repo, error) {
		return []*core.Repo{
			{ID: "r-1", ProjectID: projectID, RelativePath: "api"},
			{ID: "r-2", ProjectID: projectID, RelativePath: "web"},
		}, nil
	}
	ambiguous := newService(t, multi, nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectChangesRequest, pb.GetProjectChangesResponse]{
		{
			Name:    "a multi-repo project needs a repo_id",
			Req:     connect.NewRequest(&pb.GetProjectChangesRequest{ProjectId: "p-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, ambiguous.GetProjectChanges)
}
