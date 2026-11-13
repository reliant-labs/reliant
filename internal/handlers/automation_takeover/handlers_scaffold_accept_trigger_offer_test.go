// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// ONE RPC PER FILE. This file covers AcceptTriggerOffer and nothing else, and it
// declares no package-level identifier, so two people implementing two RPCs
// in this package never touch the same line. That is not tidiness — a
// previous scaffold put every RPC's rows and one package-level `setup()`
// into a single handlers_scaffold_test.go, two owners collided on it, and
// the whole file was deleted before either wrote a test. Anything forge
// scaffolds once has to survive the way forge's own output says to grow.
//
// TestAcceptTriggerOffer_Generated is a thin shim over `tdd.RunRPCCases`
// (== `tdd.TableRPC`, see pkg/tdd/rpc.go). Rows declare the request, the
// expected outcome, and (optionally) a Setup/Check hook — the runner
// iterates rows as t.Run subtests and asserts on either a Connect error
// code or a happy-path response.
//
// The scaffold row is SELF-DESTRUCTING: `WantScaffoldStub` matches only the
// error forge's own generated stub returns, so the row goes red the moment
// you implement AcceptTriggerOffer. That failure is the point — replace it then with
// real assertions (Check for the happy path, WantErr for failure modes).
// forge writes this file only when it is absent and AcceptTriggerOffer is still a
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
package automation_takeover_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"

	automation_takeover "github.com/reliant-labs/reliant/internal/handlers/automation_takeover"
)

// TestAcceptTriggerOffer_Generated runs the canonical scaffold rows for AcceptTriggerOffer.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestAcceptTriggerOffer_Generated(t *testing.T) {
	t.Parallel()

	// automation_takeover.NewTestAutomationTakeover (helpers_gen_test.go) returns the
	// automation_takeover *Service wired with stubbed dependencies. Override one
	// with automation_takeover.WithAutomationTakeoverDeps(...), or per-row via Case.Setup.
	svc := automation_takeover.NewTestAutomationTakeover(t, automation_takeover.WithAutomationTakeoverDeps(automation_takeover.Deps{
		Repo: &automation_takeover.MockRepository{
			GetTriggerOfferFunc: func(context.Context, string) (*core.TriggerOffer, error) { return nil, core.ErrTriggerOfferNotFound },
		},
	}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.AcceptTriggerOfferRequest, pb.AcceptTriggerOfferResponse]{
		{
			Name:    "an unknown offer is not found",
			Req:     connect.NewRequest(&pb.AcceptTriggerOfferRequest{OfferId: "missing"}),
			Ctx:     automation_takeover.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.AcceptTriggerOffer)
}
