package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// This service cannot wake a machine (control-plane's ResumeDaemon does), so
// an unattached daemon is answered, not resumed.
func TestResumeDaemon_Generated(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(registryWithDaemons()))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ResumeDaemonRequest, pb.ResumeDaemonResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ResumeDaemonRequest{DaemonId: "offline"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.ResumeDaemonRequest{}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown daemon is not found",
			Req:     connect.NewRequest(&pb.ResumeDaemonRequest{DaemonId: "missing"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's daemon is not found",
			Req:     connect.NewRequest(&pb.ResumeDaemonRequest{DaemonId: "theirs"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "an unattached daemon is not resumed here",
			Req:  connect.NewRequest(&pb.ResumeDaemonRequest{DaemonId: "offline"}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ResumeDaemonResponse]) {
				if resp.Msg.GetResumed() || resp.Msg.GetErrorMessage() == "" {
					t.Fatalf("ResumeDaemon = %v, want resumed=false with a reason", resp.Msg)
				}
			},
		},
	}, svc.ResumeDaemon)
}
