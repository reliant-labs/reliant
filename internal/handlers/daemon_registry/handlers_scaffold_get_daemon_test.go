package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// GetDaemon's happy path (stored local models, disk report, attachment)
// runs against a real database in internal/handlers/tools_daemon
// (daemon_registry_localmodel_test.go, daemon_registry_disk_test.go); these
// rows pin the checks before it.
func TestGetDaemon_Generated(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(registryWithDaemons()))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetDaemonRequest, pb.GetDaemonResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.GetDaemonRequest{DaemonId: "offline"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.GetDaemonRequest{}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown daemon is not found",
			Req:     connect.NewRequest(&pb.GetDaemonRequest{DaemonId: "missing"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's daemon is not found",
			Req:     connect.NewRequest(&pb.GetDaemonRequest{DaemonId: "theirs"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetDaemon)
}
