package project_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestGetProject_Generated(t *testing.T) {
	t.Parallel()

	unrouted := newService(t, ownedProjectRepo(t), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectRequest, pb.GetProjectResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "without a daemon the stored git flag is kept",
			Req:  connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectResponse]) {
				if got := resp.Msg.GetProject(); got.GetId() != "p-1" || got.GetPath() != creatorPath || !got.GetIsGitRepo() {
					t.Fatalf("project = %v, want p-1 at its creator path, a git repository", got)
				}
			},
		},
	}, unrouted.GetProject)

	// The daemon no longer sees a .git directory at the project path.
	store := ownedProjectRepo(t)
	var persisted *db.Project
	store.UpdateProjectFunc = func(_ context.Context, p *db.Project, _ string) error {
		persisted = p
		return nil
	}
	routed := newService(t, store, answering("project.check_git", `{"path":"/work"}`, `{"is_git_repo":false}`))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetProjectRequest, pb.GetProjectResponse]{
		{
			Name: "the daemon's observation of the git directory wins and is persisted",
			Req:  connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetProjectResponse]) {
				if resp.Msg.GetProject().GetIsGitRepo() {
					t.Fatal("project still reports a git repository the daemon no longer sees")
				}
				if persisted == nil || persisted.IsGitRepo {
					t.Fatalf("persisted = %v, want the project stored as not a git repository", persisted)
				}
			},
		},
	}, routed.GetProject)
}

// stalledDaemon never answers: it holds every command until the caller gives
// up, the way a daemon stuck in a slow filesystem walk does.
type stalledDaemon struct{ fakeDaemon }

func (stalledDaemon) SendDaemonCommand(ctx context.Context, _, _ string, _ []byte, _ int32) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Prod (2026-10-31/11-01): GetProject waited out the client's whole 10s
// deadline on project.check_git, and the client then failed the read. The
// check refreshes a cached flag; a daemon that does not answer in its budget
// costs the read that budget, and the stored flag is served.
func TestGetProject_SlowDaemonCostsOnlyTheGitCheckBudget(t *testing.T) {
	project.SetGitCheckBudget(t, 50*time.Millisecond)

	store := ownedProjectRepo(t)
	store.UpdateProjectFunc = func(context.Context, *db.Project, string) error {
		t.Error("a check that did not answer must not change the stored flag")
		return nil
	}
	svc := newService(t, store, stalledDaemon{})

	// The request's own deadline is far away, as the client's 10s was.
	ctx, cancel := context.WithTimeout(project.AuthedContext(t), 10*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := svc.GetProject(ctx, connect.NewRequest(&pb.GetProjectRequest{ProjectId: "p-1"}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !resp.Msg.GetProject().GetIsGitRepo() {
		t.Fatal("the stored git flag must be served when the daemon does not answer")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("GetProject took %s: it waited on the daemon past the git-check budget", elapsed)
	}
}
