// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// ONE RPC PER FILE. This file covers SetDefaultDaemon and nothing else, and it
// declares no package-level identifier, so two people implementing two RPCs
// in this package never touch the same line. That is not tidiness — a
// previous scaffold put every RPC's rows and one package-level `setup()`
// into a single handlers_scaffold_test.go, two owners collided on it, and
// the whole file was deleted before either wrote a test. Anything forge
// scaffolds once has to survive the way forge's own output says to grow.
//
// TestSetDefaultDaemon_Generated is a thin shim over `tdd.RunRPCCases`
// (== `tdd.TableRPC`, see pkg/tdd/rpc.go). Rows declare the request, the
// expected outcome, and (optionally) a Setup/Check hook — the runner
// iterates rows as t.Run subtests and asserts on either a Connect error
// code or a happy-path response.
//
// The scaffold row is SELF-DESTRUCTING: `WantScaffoldStub` matches only the
// error forge's own generated stub returns, so the row goes red the moment
// you implement SetDefaultDaemon. That failure is the point — replace it then with
// real assertions (Check for the happy path, WantErr for failure modes).
// forge writes this file only when it is absent and SetDefaultDaemon is still a
// stub, so a file you have edited, or an RPC you have implemented, is never
// re-offered.
//
// It does NOT match a bare `connect.CodeUnimplemented`, deliberately. A
// finished handler can return that code for its own reasons — a forwarder,
// a feature flag, an unwired optional dep — and a row keyed on the code
// alone keeps passing against fully-implemented RPCs. Asserting an
// unforgeable sentinel is what makes "nobody has written this yet"
// falsifiable.
//
// CRUD RPCs are intentionally absent — their lifecycle coverage lives in
// handlers_crud_test.go (yours as well). Each method has exactly one
// per-RPC test owner; never both.
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
// Skill: forge skill load testing/patterns
package daemon_registry_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// TestSetDefaultDaemon_Generated: the caller's own user id reaches the
// repository with the requested machine ("" clears), and the repository's
// NotFound for a machine the caller does not own reaches the wire as NotFound.
func TestSetDefaultDaemon_Generated(t *testing.T) {
	t.Parallel()

	type call struct{ userID, daemonID string }
	var calls []call
	repo := &daemon_registry.MockRepository{
		SetDefaultDaemonFunc: func(_ context.Context, userID, daemonID string) error {
			calls = append(calls, call{userID, daemonID})
			if daemonID == "theirs" {
				return svcerr.NotFound("daemon")
			}
			return nil
		},
	}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetDefaultDaemonRequest, pb.SetDefaultDaemonResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.SetDefaultDaemonRequest{DaemonId: "mine"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name: "sets the caller's default",
			Req:  connect.NewRequest(&pb.SetDefaultDaemonRequest{DaemonId: "mine"}),
			Ctx:  daemon_registry.AuthedContext(t),
		},
		{
			Name: "an empty id clears it",
			Req:  connect.NewRequest(&pb.SetDefaultDaemonRequest{}),
			Ctx:  daemon_registry.AuthedContext(t),
		},
		{
			Name:    "another user's machine is not found",
			Req:     connect.NewRequest(&pb.SetDefaultDaemonRequest{DaemonId: "theirs"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.SetDefaultDaemon)

	assert.Equal(t, []call{{"test-user", "mine"}, {"test-user", ""}, {"test-user", "theirs"}}, calls)
}
