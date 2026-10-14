package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestPullProjectOnDaemon_Generated(t *testing.T) {
	t.Parallel()

	store := onDaemon(t)
	var refreshed int
	store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error {
		refreshed++
		return nil
	}
	svc := newService(t, store, answering("git.pull", `{"branch":"main","path":"/clone"}`,
		`{"success":true,"output":"Already up to date."}`))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PullProjectOnDaemonRequest, pb.PullProjectOnDaemonResponse]{
		{
			Name:    "project_id and daemon_id are required",
			Req:     connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: "p-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: "p-2", DaemonId: "d-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a daemon without a clone of the project is not found",
			Req:     connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-2"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the clone is pulled on its branch and its clone time refreshed",
			Req:  connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.PullProjectOnDaemonResponse]) {
				if resp.Msg.GetOutput() != "Already up to date." || refreshed != 1 {
					t.Fatalf("output = %q, refreshed %d times, want git's output and one refresh", resp.Msg.GetOutput(), refreshed)
				}
			},
		},
	}, svc.PullProjectOnDaemon)
}
