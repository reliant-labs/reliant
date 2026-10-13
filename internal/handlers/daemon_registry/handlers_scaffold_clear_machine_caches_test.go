package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// The relay to an attached machine runs against a real database in
// internal/grpc/services/daemon_registry_disk_test.go
// (TestClearMachineCaches_RelaysToTheMachine); these rows pin the checks
// before it.
func TestClearMachineCaches_Generated(t *testing.T) {
	t.Parallel()

	kinds := []string{"go_build_cache"}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(registryWithDaemons()))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ClearMachineCachesRequest, pb.ClearMachineCachesResponse]{
		{
			Name:    "kinds is required",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{DaemonId: "offline"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{DaemonId: "offline", Kinds: kinds}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{Kinds: kinds}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown daemon is not found",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{DaemonId: "missing", Kinds: kinds}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's daemon is not found",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{DaemonId: "theirs", Kinds: kinds}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an offline daemon is unavailable",
			Req:     connect.NewRequest(&pb.ClearMachineCachesRequest{DaemonId: "offline", Kinds: kinds}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, svc.ClearMachineCaches)
}
