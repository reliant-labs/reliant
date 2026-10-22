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

func TestListProjectDaemons_Generated(t *testing.T) {
	t.Parallel()

	store := &project.MockRepository{
		ListProjectsFunc: func(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
			if f.UserID != "test-user" {
				return nil, nil
			}
			return []*db.Project{ownedProject()}, nil
		},
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			return []*core.ProjectDaemon{
				{ProjectID: projectID, DaemonID: "d-1", Path: "/work"},
				{ProjectID: projectID, DaemonID: "d-2", Path: "/work", InstallState: core.ProjectInstallInstalling},
			}, nil
		},
	}
	svc := newService(t, store, nil)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListProjectDaemonsRequest, pb.ListProjectDaemonsResponse]{
		{
			Name: "every daemon of every one of the caller's projects is listed, with its install state",
			Req:  connect.NewRequest(&pb.ListProjectDaemonsRequest{}),
			Ctx:  project.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListProjectDaemonsResponse]) {
				got := resp.Msg.GetProjectDaemons()
				if len(got) != 2 || got[0].GetDaemonId() != "d-1" || got[1].GetDaemonId() != "d-2" {
					t.Fatalf("project daemons = %v, want p-1 on d-1 and d-2", got)
				}
				// A row from before install states existed is installed.
				if got[0].GetInstallState() != pb.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED ||
					got[1].GetInstallState() != pb.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING {
					t.Fatalf("install states = %v, %v", got[0].GetInstallState(), got[1].GetInstallState())
				}
			},
		},
	}, svc.ListProjectDaemons)
}
