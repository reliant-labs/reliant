package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// Matching by name and hostname is pinned in daemon_registry_status_test.go
// (TestDaemonAnswersToName); these rows pin the caller check and the
// not-found answer, which is a response, not an error.
func TestResolveDaemon_Generated(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ResolveDaemonRequest, pb.ResolveDaemonResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ResolveDaemonRequest{DaemonName: "laptop"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "no matching daemon answers found=false",
			Req:  connect.NewRequest(&pb.ResolveDaemonRequest{DaemonName: "laptop"}),
			Ctx:  daemon_registry.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ResolveDaemonResponse]) {
				if resp.Msg.GetFound() || resp.Msg.GetDaemon() != nil {
					t.Fatalf("ResolveDaemon = %v, want found=false and no daemon", resp.Msg)
				}
			},
		},
	}, svc.ResolveDaemon)
}
