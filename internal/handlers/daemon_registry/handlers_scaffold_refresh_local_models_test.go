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
// internal/handlers/tools_daemon/daemon_registry_localmodel_test.go
// (TestDaemonRegistry_RefreshLocalModels); these rows pin the checks before
// it.
func TestRefreshLocalModels_Generated(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(registryWithDaemons()))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RefreshLocalModelsRequest, pb.RefreshLocalModelsResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.RefreshLocalModelsRequest{DaemonId: "offline"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.RefreshLocalModelsRequest{}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown daemon is not found",
			Req:     connect.NewRequest(&pb.RefreshLocalModelsRequest{DaemonId: "missing"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's daemon is not found",
			Req:     connect.NewRequest(&pb.RefreshLocalModelsRequest{DaemonId: "theirs"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an offline daemon is unavailable",
			Req:     connect.NewRequest(&pb.RefreshLocalModelsRequest{DaemonId: "offline"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, svc.RefreshLocalModels)
}
