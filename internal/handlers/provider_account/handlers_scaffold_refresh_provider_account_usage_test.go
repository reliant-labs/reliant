// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// ONE RPC PER FILE. This file covers RefreshProviderAccountUsage and nothing else, and it
// declares no package-level identifier, so two people implementing two RPCs
// in this package never touch the same line. That is not tidiness — a
// previous scaffold put every RPC's rows and one package-level `setup()`
// into a single handlers_scaffold_test.go, two owners collided on it, and
// the whole file was deleted before either wrote a test. Anything forge
// scaffolds once has to survive the way forge's own output says to grow.
//
// TestRefreshProviderAccountUsage_Generated is a thin shim over `tdd.RunRPCCases`
// (== `tdd.TableRPC`, see pkg/tdd/rpc.go). Rows declare the request, the
// expected outcome, and (optionally) a Setup/Check hook — the runner
// iterates rows as t.Run subtests and asserts on either a Connect error
// code or a happy-path response.
//
// The scaffold row is SELF-DESTRUCTING: `WantScaffoldStub` matches only the
// error forge's own generated stub returns, so the row goes red the moment
// you implement RefreshProviderAccountUsage. That failure is the point — replace it then with
// real assertions (Check for the happy path, WantErr for failure modes).
// forge writes this file only when it is absent and RefreshProviderAccountUsage is still a
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
package provider_account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	provider_account "github.com/reliant-labs/reliant/internal/handlers/provider_account"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
)

type fakeRefresher struct {
	res  accountlimits.RefreshResult
	err  error
	user string
	id   string
}

func (f *fakeRefresher) RefreshUsage(_ context.Context, userID, accountID string) (accountlimits.RefreshResult, error) {
	f.user, f.id = userID, accountID
	return f.res, f.err
}

func TestRefreshProviderAccountUsage_Generated(t *testing.T) {
	t.Parallel()

	used := 0.42
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	ok := &fakeRefresher{res: accountlimits.RefreshResult{
		Account: core.ProviderAccount{ID: "acct-1", Provider: "claude", AuthKind: core.AuthKindOAuth, Email: "alice@example.com", Plan: "max_20x"},
		Limits: &core.LimitState{AccountID: "acct-1", Source: accountlimits.SourceUsageAPI, ObservedAt: time.Now(),
			Windows: []core.LimitWindow{{Name: "5h", UsedFraction: &used, ResetsAt: until, LengthS: 5 * 3600}}},
	}}
	throttled := &fakeRefresher{res: accountlimits.RefreshResult{
		Account: core.ProviderAccount{ID: "acct-1", Provider: "claude", AuthKind: core.AuthKindOAuth}, Throttled: true,
	}}
	missing := &fakeRefresher{err: core.ErrProviderAccountNotFound}
	broken := &fakeRefresher{err: errors.New("database unreachable")}

	svc := func(r accountlimits.Refresher) *provider_account.Service {
		return provider_account.NewTestProviderAccount(t, provider_account.WithProviderAccountDeps(provider_account.Deps{UsageRefresher: r}))
	}
	req := func(id string) *connect.Request[pb.RefreshProviderAccountUsageRequest] {
		return connect.NewRequest(&pb.RefreshProviderAccountUsageRequest{AccountId: id})
	}
	run := func(s *provider_account.Service, name string, c tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]) {
		t.Run(name, func(t *testing.T) {
			c.Name = name
			tdd.RunRPCCases(t, []tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{c}, s.RefreshProviderAccountUsage)
		})
	}

	run(svc(ok), "returns the refreshed account scoped to the caller", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("acct-1"), Ctx: provider_account.AuthedContext(t),
		Check: func(t *testing.T, resp *connect.Response[pb.RefreshProviderAccountUsageResponse]) {
			if ok.user != "test-user" || ok.id != "acct-1" {
				t.Errorf("refresher got user=%q account=%q", ok.user, ok.id)
			}
			if resp.Msg.GetThrottled() {
				t.Error("throttled should be false")
			}
			a := resp.Msg.GetAccount()
			if a.GetId() != "acct-1" || a.GetPlanDisplay() != "Max 20×" || len(a.GetWindows()) != 1 {
				t.Fatalf("unexpected account: %v", a)
			}
			if w := a.GetWindows()[0]; w.GetLabel() != "5-hour" || w.GetUsedFraction() != 0.42 || w.GetResetsAt() != until.Format(time.RFC3339) {
				t.Errorf("unexpected window: %v", w)
			}
			if a.GetUsageSource() != pb.UsageSource_USAGE_SOURCE_USAGE_API {
				t.Errorf("usage source = %v", a.GetUsageSource())
			}
		},
	})
	run(svc(throttled), "within the floor the stored state comes back throttled", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("acct-1"), Ctx: provider_account.AuthedContext(t),
		Check: func(t *testing.T, resp *connect.Response[pb.RefreshProviderAccountUsageResponse]) {
			if !resp.Msg.GetThrottled() || resp.Msg.GetAccount().GetId() != "acct-1" {
				t.Errorf("unexpected response: %v", resp.Msg)
			}
		},
	})
	run(svc(ok), "an empty account id is invalid", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req(""), Ctx: provider_account.AuthedContext(t), WantErr: connect.CodeInvalidArgument,
	})
	run(svc(missing), "another user's or an unknown account is NotFound, never PermissionDenied", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("someone-elses"), Ctx: provider_account.AuthedContext(t), WantErr: connect.CodeNotFound,
	})
	run(svc(broken), "a store failure is internal", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("acct-1"), Ctx: provider_account.AuthedContext(t), WantErr: connect.CodeInternal,
	})
	run(svc(nil), "a server without the account store is unavailable", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("acct-1"), Ctx: provider_account.AuthedContext(t), WantErr: connect.CodeUnavailable,
	})
	run(svc(ok), "anonymous callers are refused", tdd.RPCCase[pb.RefreshProviderAccountUsageRequest, pb.RefreshProviderAccountUsageResponse]{
		Req: req("acct-1"), Ctx: context.Background(), WantErr: connect.CodeUnauthenticated,
	})
}
