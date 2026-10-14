package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestMarkProjectInstalled_Generated(t *testing.T) {
	t.Parallel()

	store := ownedProjectRepo(t)
	var upserted []string
	store.UpsertProjectDaemonFunc = func(_ context.Context, projectID, daemonID, path string, branch *string) error {
		upserted = append(upserted, projectID+" "+daemonID+" "+path+" "+ptr.From(branch))
		return nil
	}
	// Without a daemon router the forge name is left for a later read.
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.MarkProjectInstalledRequest, pb.MarkProjectInstalledResponse]{
		{
			Name:    "project_id, daemon_id and path are required",
			Req:     connect.NewRequest(&pb.MarkProjectInstalledRequest{ProjectId: "p-1", DaemonId: "d-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.MarkProjectInstalledRequest{ProjectId: "p-2", DaemonId: "d-1", Path: "/work"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the install is recorded and returned",
			Req: connect.NewRequest(&pb.MarkProjectInstalledRequest{
				ProjectId: "p-1", DaemonId: "d-1", Path: "/work", DefaultBranch: ptr.Of("main"),
			}),
			Ctx: project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.MarkProjectInstalledResponse]) {
				if len(upserted) != 1 || upserted[0] != "p-1 d-1 /work main" {
					t.Fatalf("upserted = %v, want p-1 on d-1 at /work on main", upserted)
				}
				if got := resp.Msg.GetProjectDaemon(); got.GetProjectId() != "p-1" || got.GetDaemonId() != "d-1" || got.GetDefaultBranch() != "main" {
					t.Fatalf("project daemon = %v", got)
				}
			},
		},
	}, svc.MarkProjectInstalled)
}
