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

func TestListProjectDaemonsForDaemon_Generated(t *testing.T) {
	t.Parallel()

	// d-1 holds the caller's p-1 and another user's p-2.
	store := ownedProjectRepo(t)
	store.ListProjectDaemonsForDaemonFunc = func(_ context.Context, daemonID string) ([]*core.ProjectDaemon, error) {
		return []*core.ProjectDaemon{
			{ProjectID: "p-1", DaemonID: daemonID, Path: "/work"},
			{ProjectID: "p-2", DaemonID: daemonID, Path: "/theirs"},
		}, nil
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListProjectDaemonsForDaemonRequest, pb.ListProjectDaemonsForDaemonResponse]{
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.ListProjectDaemonsForDaemonRequest{}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "only the caller's projects on the daemon are listed",
			Req:  connect.NewRequest(&pb.ListProjectDaemonsForDaemonRequest{DaemonId: "d-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListProjectDaemonsForDaemonResponse]) {
				got := resp.Msg.GetProjectDaemons()
				if len(got) != 1 || got[0].GetProjectId() != "p-1" || got[0].GetDaemonId() != "d-1" {
					t.Fatalf("project daemons = %v, want only p-1 on d-1", got)
				}
			},
		},
	}, svc.ListProjectDaemonsForDaemon)
}
