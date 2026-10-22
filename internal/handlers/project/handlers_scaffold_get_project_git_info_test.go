package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProjectGitInfo_Generated(t *testing.T) {
	t.Parallel()

	unrouted := newService(t, ownedProjectRepo(t), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectGitInfoRequest, pb.GetProjectGitInfoResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without a daemon there is no git to read",
			Req:     connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeUnavailable, // a daemon command that could not run (daemonerr), not a server fault
		},
	}, unrouted.GetProjectGitInfo)

	plain := ownedProjectRepo(t)
	plain.GetProjectFunc = func(context.Context, string) (*db.Project, error) {
		p := ownedProject()
		p.IsGitRepo = false
		return p, nil
	}
	notGit := newService(t, plain, nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectGitInfoRequest, pb.GetProjectGitInfoResponse]{
		{
			Name: "a project that is not a git repository says so without asking the daemon",
			Req:  connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectGitInfoResponse]) {
				if resp.Msg.GetIsGitRepo() || resp.Msg.GetProjectId() != "p-1" {
					t.Fatalf("response = %v, want p-1, not a git repository", resp.Msg)
				}
			},
		},
	}, notGit.GetProjectGitInfo)

	// p-1 holds one repo, at svc/: its checkout is where git runs.
	nested := ownedProjectRepo(t)
	nested.ListReposByProjectFunc = func(_ context.Context, projectID string) ([]*core.Repo, error) {
		return []*core.Repo{{ID: "r-1", ProjectID: projectID, RelativePath: "svc"}}, nil
	}
	routed := newService(t, nested, answering("project.git_info", `{"path":"/work/svc"}`,
		`{"current_branch":"feature","remote_url":"git@example.com:acme/svc.git","has_changes":true,"ahead":2}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectGitInfoRequest, pb.GetProjectGitInfoResponse]{
		{
			Name:    "a repo outside the project is not found",
			Req:     connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1", RepoId: "r-9"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the daemon reports the repo's checkout",
			Req:  connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectGitInfoResponse]) {
				m := resp.Msg
				if !m.GetIsGitRepo() || m.GetCurrentBranch() != "feature" || !m.GetHasChanges() || m.GetAhead() != 2 || m.GetRemoteUrl() != "git@example.com:acme/svc.git" {
					t.Fatalf("git info = %v, want the daemon's report for svc/", m)
				}
			},
		},
	}, routed.GetProjectGitInfo)
}
