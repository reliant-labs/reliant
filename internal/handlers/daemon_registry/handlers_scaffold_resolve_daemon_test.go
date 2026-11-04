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

// Matching by name and hostname is pinned in daemon_registry_status_test.go
// (TestDaemonAnswersToName); these rows pin the caller check and the
// not-found answer, which is a response, not an error.
func TestResolveDaemon_Generated(t *testing.T) {
	t.Parallel()

	// The caller has no daemons. The harness's auto-stub refuses an
	// unprogrammed call, so the one listing this rpc makes is programmed.
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{
		Repo: &daemon_registry.MockRepository{
			ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return nil, nil },
			ListFreshDaemonAttachmentsForUserFunc: func(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
				return nil, nil
			},
		},
	}))
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

// A request naming no machine answers the user's default, asleep or not, over
// an attached machine listed first; a request naming one is unaffected.
func TestResolveDaemon_NoSelectorAnswersTheDefault(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{
		Repo: &daemon_registry.MockRepository{
			ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) {
				return []*db.Daemon{
					{ID: "cicd", UserID: "test-user"},
					{ID: "default", UserID: "test-user", IsDefault: true},
				}, nil
			},
			ListFreshDaemonAttachmentsForUserFunc: func(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
				return []*db.DaemonAttachment{{DaemonID: "cicd"}}, nil
			},
		},
	}))
	resolved := func(want string) func(*testing.T, *connect.Response[pb.ResolveDaemonResponse]) {
		return func(t *testing.T, resp *connect.Response[pb.ResolveDaemonResponse]) {
			if !resp.Msg.GetFound() || resp.Msg.GetDaemon().GetDaemonId() != want {
				t.Fatalf("ResolveDaemon = %v, want %s", resp.Msg, want)
			}
		}
	}
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ResolveDaemonRequest, pb.ResolveDaemonResponse]{
		{
			Name:  "no selector is the default",
			Req:   connect.NewRequest(&pb.ResolveDaemonRequest{}),
			Ctx:   daemon_registry.AuthedContext(t),
			Check: resolved("default"),
		},
		{
			Name:  "an id selector is its own",
			Req:   connect.NewRequest(&pb.ResolveDaemonRequest{DaemonId: "cicd"}),
			Ctx:   daemon_registry.AuthedContext(t),
			Check: resolved("cicd"),
		},
	}, svc.ResolveDaemon)
}
