// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package project_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
)

// TestPreviewProjectMerge_Generated holds who may preview a merge (both
// projects the caller's own, writable, live and in one org) and that the
// store's counts and blockers reach the wire.
func TestPreviewProjectMerge_Generated(t *testing.T) {
	t.Parallel()

	acme, globex := "acme", "globex"
	deleted := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	projects := map[string]*db.Project{
		"p-a":       {ID: "p-a", UserID: "test-user"},
		"p-b":       {ID: "p-b", UserID: "test-user"},
		"p-theirs":  {ID: "p-theirs", UserID: "someone-else"},
		"p-deleted": {ID: "p-deleted", UserID: "test-user", DeleteAfter: &deleted},
		"p-acme":    {ID: "p-acme", UserID: "test-user", OrgID: &acme},
		"p-globex":  {ID: "p-globex", UserID: "test-user", OrgID: &globex},
	}
	store := &stubRepo{MockRepository: &project.MockRepository{},
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if p, ok := projects[id]; ok {
				return p, nil
			}
			return nil, svcerr.ErrNotFound
		},
	}
	merges := &project.MockProjectMerges{
		PreviewProjectMergeFunc: func(_ context.Context, source, target string) (*core.ProjectMergePreview, error) {
			if source != "p-a" || target != "p-b" {
				t.Errorf("previewed %s into %s", source, target)
			}
			return &core.ProjectMergePreview{
				Counts: core.ProjectMergeCounts{Chats: 3, Worktrees: 2, Triggers: 1, Repos: 2, OtherRows: 5,
					Checkouts: []core.ProjectMergeCheckout{{DaemonID: "d-1", InstallState: core.ProjectInstallInstalled}}},
				Blockers: []core.ProjectMergeBlocker{
					{Kind: core.ProjectMergeSameMachine, Message: "both on d-2", DaemonID: "d-2"},
					{Kind: core.ProjectMergeRunningWork, Message: "a chat runs", ChatID: "c-1"},
				},
			}, nil
		},
	}
	svc := project.NewTestProject(t, withDeps(project.Deps{Repo: store, Projects: projectaccesstest.For(store), Merges: merges}))
	req := func(source, target string) *connect.Request[pb.PreviewProjectMergeRequest] {
		return connect.NewRequest(&pb.PreviewProjectMergeRequest{SourceProjectId: source, TargetProjectId: target})
	}
	member := authz.WithMembership(project.AuthedContext(t), moveOrgs{})

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PreviewProjectMergeRequest, pb.PreviewProjectMergeResponse]{
		{
			Name: "two of the caller's projects",
			Req:  req("p-a", "p-b"),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.PreviewProjectMergeResponse]) {
				c := resp.Msg.GetCounts()
				if c.GetChats() != 3 || c.GetWorktrees() != 2 || c.GetTriggers() != 1 || c.GetRepos() != 2 || c.GetOtherRows() != 5 {
					t.Fatalf("counts = %v", c)
				}
				if len(c.GetCheckouts()) != 1 || c.GetCheckouts()[0].GetDaemonId() != "d-1" ||
					c.GetCheckouts()[0].GetInstallState() != pb.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED {
					t.Fatalf("checkouts = %v", c.GetCheckouts())
				}
				b := resp.Msg.GetBlockers()
				if len(b) != 2 ||
					b[0].GetKind() != pb.ProjectMergeBlockerKind_PROJECT_MERGE_BLOCKER_KIND_SAME_MACHINE || b[0].GetDaemonId() != "d-2" || b[0].ChatId != nil ||
					b[1].GetKind() != pb.ProjectMergeBlockerKind_PROJECT_MERGE_BLOCKER_KIND_RUNNING_WORK || b[1].GetChatId() != "c-1" || b[1].DaemonId != nil ||
					b[1].GetMessage() != "a chat runs" {
					t.Fatalf("blockers = %v", b)
				}
			},
		},
		{Name: "another user's source", Req: req("p-theirs", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "another user's target", Req: req("p-a", "p-theirs"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "a target scheduled for deletion", Req: req("p-a", "p-deleted"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "a source that does not exist", Req: req("p-missing", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound},
		{Name: "a project into itself", Req: req("p-a", "p-a"), Ctx: project.AuthedContext(t), WantErr: connect.CodeFailedPrecondition},
		{Name: "projects of two organizations", Req: req("p-acme", "p-globex"), Ctx: member, WantErr: connect.CodeFailedPrecondition},
		{Name: "a project of an org and one of none", Req: req("p-acme", "p-b"), Ctx: member, WantErr: connect.CodeFailedPrecondition},
		{Name: "no source", Req: req("", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeInvalidArgument},
	}, svc.PreviewProjectMerge)
}
