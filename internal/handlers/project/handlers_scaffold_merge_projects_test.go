// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package project_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
)

// TestMergeProjects_Generated holds who may merge (as the preview admits),
// what the handler asks the store for (the owner and org the projects were
// admitted on, the caller as the deleter, the restore window), and how a
// blocked merge answers.
func TestMergeProjects_Generated(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	deleted := now.Add(-time.Hour)
	projects := map[string]*db.Project{
		"p-a":       {ID: "p-a", UserID: "test-user", Name: "shop"},
		"p-b":       {ID: "p-b", UserID: "test-user", Name: "shop-2"},
		"p-busy":    {ID: "p-busy", UserID: "test-user", Name: "busy"},
		"p-theirs":  {ID: "p-theirs", UserID: "someone-else"},
		"p-deleted": {ID: "p-deleted", UserID: "test-user", DeleteAfter: &deleted},
	}
	store := &stubRepo{MockRepository: &project.MockRepository{},
		GetProjectFunc: func(_ context.Context, id string) (*db.Project, error) {
			if p, ok := projects[id]; ok {
				return p, nil
			}
			return nil, svcerr.ErrNotFound
		},
	}
	var merged atomic.Int32
	merges := &project.MockProjectMerges{
		MergeProjectsFunc: func(_ context.Context, m core.ProjectMerge) (*core.ProjectMergeResult, error) {
			merged.Add(1)
			if m.SourceID == "p-busy" {
				return &core.ProjectMergeResult{Blockers: []core.ProjectMergeBlocker{
					{Kind: core.ProjectMergeRunningWork, Message: `the chat "release" in "busy" is running`, ChatID: "c-1"},
				}}, core.ErrProjectMergeBlocked
			}
			if m.SourceID != "p-a" || m.TargetID != "p-b" || m.ActorUserID != "test-user" ||
				m.Decided.OwnerUserID != "test-user" || m.Decided.OrgID != nil ||
				!m.DeleteAfter.Equal(now.Add(core.ProjectDeleteWindow)) {
				t.Errorf("merge asked = %+v", m)
			}
			return &core.ProjectMergeResult{
				Counts:      core.ProjectMergeCounts{Chats: 3, Worktrees: 2, Triggers: 1, Repos: 2, OtherRows: 5},
				DeleteAfter: m.DeleteAfter,
			}, nil
		},
	}
	svc := project.NewTestProject(t, withDeps(project.Deps{
		Repo: store, Projects: projectaccesstest.For(store), Merges: merges, Now: func() time.Time { return now },
	}))
	req := func(source, target string) *connect.Request[pb.MergeProjectsRequest] {
		return connect.NewRequest(&pb.MergeProjectsRequest{SourceProjectId: source, TargetProjectId: target})
	}
	refusedBeforeTheStore := func(t *testing.T) {
		before := merged.Load()
		t.Cleanup(func() {
			if merged.Load() != before {
				t.Errorf("a refused merge reached the store")
			}
		})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.MergeProjectsRequest, pb.MergeProjectsResponse]{
		{
			Name: "two of the caller's projects",
			Req:  req("p-a", "p-b"),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.MergeProjectsResponse]) {
				if resp.Msg.GetProject().GetId() != "p-b" {
					t.Fatalf("project = %v, want the target", resp.Msg.GetProject())
				}
				c := resp.Msg.GetCounts()
				if c.GetChats() != 3 || c.GetWorktrees() != 2 || c.GetTriggers() != 1 || c.GetRepos() != 2 || c.GetOtherRows() != 5 {
					t.Fatalf("counts = %v", c)
				}
			},
		},
		{Name: "work running in the source", Req: req("p-busy", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeFailedPrecondition},
		{Name: "another user's target", Req: req("p-a", "p-theirs"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound, Setup: refusedBeforeTheStore},
		{Name: "a source scheduled for deletion", Req: req("p-deleted", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeNotFound, Setup: refusedBeforeTheStore},
		{Name: "a project into itself", Req: req("p-b", "p-b"), Ctx: project.AuthedContext(t), WantErr: connect.CodeFailedPrecondition, Setup: refusedBeforeTheStore},
	}, svc.MergeProjects)

	// A blocked merge says what blocks it, and why, in its refusal.
	t.Run("a blocked merge names its blockers", func(t *testing.T) {
		_, err := svc.MergeProjects(project.AuthedContext(t), req("p-busy", "p-b"))
		if !strings.Contains(err.Error(), `the chat "release" in "busy" is running`) {
			t.Fatalf("err = %v, want it to name the blocker", err)
		}
		if got := svcerr.ToConnect(err).Code(); got != connect.CodeFailedPrecondition {
			t.Fatalf("code = %v", got)
		}
	})
}
