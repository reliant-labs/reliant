package project_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
)

func TestRecloneProjectOnDaemon_Generated(t *testing.T) {
	t.Parallel()

	store := onDaemon(t)
	var refreshed int
	store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error {
		refreshed++
		return nil
	}
	svc := newService(t, store, answering("git.reclone",
		`{"branch":"main","op_id":"op-1","path":"/clone","repo":"git@example.com:acme/proj.git"}`, `{"success":true,"path":"/clone"}`))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.RecloneProjectOnDaemonRequest, pb.RecloneProjectOnDaemonResponse]{
		{
			Name:    "a daemon without a clone of the project is not found",
			Req:     &pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-2"},
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the clone is recloned from the project's remote",
			Req:  &pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"},
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.RecloneProjectOnDaemonResponse) {
				if resp.GetPath() != "/clone" || refreshed != 1 {
					t.Fatalf("path = %q, refreshed %d times, want /clone and one refresh", resp.GetPath(), refreshed)
				}
			},
		},
	}, svc.RecloneProjectOnDaemon)

	// A project with no remote has nothing to clone from.
	local := onDaemon(t)
	local.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) {
		p := ownedProject()
		p.RemoteURL = nil
		return p, nil
	}
	noRemote := newService(t, local, nil)
	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.RecloneProjectOnDaemonRequest, pb.RecloneProjectOnDaemonResponse]{
		{
			Name:    "a project without a remote cannot be recloned",
			Req:     &pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"},
			Ctx:     project.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, noRemote.RecloneProjectOnDaemon)
}
