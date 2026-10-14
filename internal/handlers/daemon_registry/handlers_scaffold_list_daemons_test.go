package daemon_registry_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// The listing itself (status composition, stored local models, disk
// reports, liveness read failures) runs against a real database in
// internal/handlers/tools_daemon (daemon_registry_localmodel_test.go,
// daemon_registry_disk_test.go, tools_daemon_lease_test.go); these rows pin
// the caller check and the empty answer.
func TestListDaemons_Generated(t *testing.T) {
	t.Parallel()

	// The caller has no daemons. The harness's auto-stub refuses an
	// unprogrammed call, so the one listing this rpc makes is programmed.
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{
		Repo: &daemon_registry.StubRepo{
			ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return nil, nil },
			ListFreshDaemonAttachmentsForUserFunc: func(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
				return nil, nil
			},
		},
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListDaemonsRequest, pb.ListDaemonsResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ListDaemonsRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "a caller with no daemons gets an empty list",
			Req:  connect.NewRequest(&pb.ListDaemonsRequest{}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListDaemonsResponse]) {
				if n := len(resp.Msg.GetDaemons()); n != 0 {
					t.Fatalf("ListDaemons returned %d daemons, want 0", n)
				}
			},
		},
	}, svc.ListDaemons)
}
