// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// ONE RPC PER FILE. This file covers GetContentBlockBodies and nothing else, and it
// declares no package-level identifier, so two people implementing two RPCs
// in this package never touch the same line. That is not tidiness — a
// previous scaffold put every RPC's rows and one package-level `setup()`
// into a single handlers_scaffold_test.go, two owners collided on it, and
// the whole file was deleted before either wrote a test. Anything forge
// scaffolds once has to survive the way forge's own output says to grow.
//
// TestGetContentBlockBodies_Generated is a thin shim over `tdd.RunRPCCases`
// (== `tdd.TableRPC`, see pkg/tdd/rpc.go). Rows declare the request, the
// expected outcome, and (optionally) a Setup/Check hook — the runner
// iterates rows as t.Run subtests and asserts on either a Connect error
// code or a happy-path response.
//
// The scaffold row is SELF-DESTRUCTING: `WantScaffoldStub` matches only the
// error forge's own generated stub returns, so the row goes red the moment
// you implement GetContentBlockBodies. That failure is the point — replace it then with
// real assertions (Check for the happy path, WantErr for failure modes).
// forge writes this file only when it is absent and GetContentBlockBodies is still a
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
package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// TestGetContentBlockBodies_Generated runs the canonical scaffold rows for GetContentBlockBodies.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestGetContentBlockBodies_Generated(t *testing.T) {
	t.Parallel()

	// chat.NewTestChat (helpers_gen_test.go) returns the
	// chat *Service wired with stubbed dependencies. Override one
	// with chat.WithChatDeps(...), or per-row via Case.Setup.
	svc := chat.NewTestChat(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetContentBlockBodiesRequest, pb.GetContentBlockBodiesResponse]{
		{
			Name:    "chat_id_required",
			Req:     connect.NewRequest(&pb.GetContentBlockBodiesRequest{BlockIds: []string{"b1"}}),
			Ctx:     chat.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "more_than_100_ids",
			Req:     connect.NewRequest(&pb.GetContentBlockBodiesRequest{ChatId: "c1", BlockIds: make([]string, 101)}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.GetContentBlockBodies)
}
