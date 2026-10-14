package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestRemoveProjectFromDaemon_Generated(t *testing.T) {
	t.Parallel()

	store := onDaemon(t)
	var deleted []string
	store.DeleteProjectDaemonFunc = func(_ context.Context, projectID, daemonID string) error {
		deleted = append(deleted, projectID+" "+daemonID)
		return nil
	}
	svc := newService(t, store, answering("git.remove", `{"path":"/clone"}`, `{"success":true,"removed":true}`))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RemoveProjectFromDaemonRequest, pb.RemoveProjectFromDaemonResponse]{
		{
			Name:    "project_id and daemon_id are required",
			Req:     connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{DaemonId: "d-1"}),
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "removing a clone that is not there succeeds and deletes nothing",
			Req:  connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-2"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, _ *connect.Response[pb.RemoveProjectFromDaemonResponse]) {
				if len(deleted) != 0 {
					t.Fatalf("deleted = %v, want nothing", deleted)
				}
			},
		},
		{
			Name: "the clone is removed from disk and its row deleted",
			Req:  connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, _ *connect.Response[pb.RemoveProjectFromDaemonResponse]) {
				if len(deleted) != 1 || deleted[0] != "p-1 d-1" {
					t.Fatalf("deleted = %v, want p-1 on d-1", deleted)
				}
			},
		},
	}, svc.RemoveProjectFromDaemon)
}
