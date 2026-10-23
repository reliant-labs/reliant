package tdd_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"
)

// fakeReq / fakeResp are stand-ins for proto messages — TableRPC never
// inspects them, only passes them through, so a struct is enough.
type fakeReq struct{ Name string }
type fakeResp struct{ Greeting string }

func helloHandler(_ context.Context, req *connect.Request[fakeReq]) (*connect.Response[fakeResp], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name required"))
	}
	if req.Msg.Name == "boom" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("kaboom"))
	}
	return connect.NewResponse(&fakeResp{Greeting: "hi " + req.Msg.Name}), nil
}

func TestTableRPC_HappyAndError(t *testing.T) {
	cases := []tdd.Case[fakeReq, fakeResp]{
		{
			Name: "happy",
			Req:  connect.NewRequest(&fakeReq{Name: "ada"}),
			Check: func(t *testing.T, resp *connect.Response[fakeResp]) {
				if resp.Msg.Greeting != "hi ada" {
					t.Fatalf("got %q, want %q", resp.Msg.Greeting, "hi ada")
				}
			},
		},
		{
			Name:    "missing name → invalid argument",
			Req:     connect.NewRequest(&fakeReq{}),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "boom → internal",
			Req:     connect.NewRequest(&fakeReq{Name: "boom"}),
			WantErr: connect.CodeInternal,
		},
	}

	tdd.TableRPC(t, cases, helloHandler)
}

func TestTableRPC_SetupRuns(t *testing.T) {
	var setupCalls int
	cases := []tdd.Case[fakeReq, fakeResp]{
		{
			Name:  "row 1",
			Req:   connect.NewRequest(&fakeReq{Name: "x"}),
			Setup: func(_ *testing.T) { setupCalls++ },
		},
		{
			Name:  "row 2",
			Req:   connect.NewRequest(&fakeReq{Name: "y"}),
			Setup: func(_ *testing.T) { setupCalls++ },
		},
	}
	tdd.TableRPC(t, cases, helloHandler)
	if setupCalls != 2 {
		t.Fatalf("setup ran %d times, want 2", setupCalls)
	}
}

func TestRunRPCCases_AliasMatchesTableRPC(t *testing.T) {
	// RunRPCCases is the codegen-facing alias of TableRPC. Verify it
	// runs identically: error-code rows, happy-path rows, multiple
	// cases per test, and per-case Setup hooks executing in declared
	// order.
	var setupOrder []string
	cases := []tdd.RPCCase[fakeReq, fakeResp]{
		{
			Name:  "happy",
			Req:   connect.NewRequest(&fakeReq{Name: "ada"}),
			Setup: func(_ *testing.T) { setupOrder = append(setupOrder, "happy") },
			Check: func(t *testing.T, resp *connect.Response[fakeResp]) {
				if resp.Msg.Greeting != "hi ada" {
					t.Fatalf("got %q, want %q", resp.Msg.Greeting, "hi ada")
				}
			},
		},
		{
			Name:    "missing → invalid argument",
			Req:     connect.NewRequest(&fakeReq{}),
			WantErr: connect.CodeInvalidArgument,
			Setup:   func(_ *testing.T) { setupOrder = append(setupOrder, "missing") },
		},
		{
			Name:    "boom → internal",
			Req:     connect.NewRequest(&fakeReq{Name: "boom"}),
			WantErr: connect.CodeInternal,
			Setup:   func(_ *testing.T) { setupOrder = append(setupOrder, "boom") },
		},
	}

	tdd.RunRPCCases(t, cases, helloHandler)

	if want := []string{"happy", "missing", "boom"}; len(setupOrder) != len(want) {
		t.Fatalf("setup ran %v, want %v", setupOrder, want)
	} else {
		for i := range want {
			if setupOrder[i] != want[i] {
				t.Fatalf("setup[%d] = %q, want %q", i, setupOrder[i], want[i])
			}
		}
	}
}

func TestRunRPCCases_RPCCaseIsCaseAlias(t *testing.T) {
	// Compile-time check: RPCCase must be a type *alias* (assignable
	// from Case without conversion), not a named-type wrapper.
	var c tdd.Case[fakeReq, fakeResp]
	var rc tdd.RPCCase[fakeReq, fakeResp] = c // assigns iff alias
	_ = rc
}

func TestTableRPC_ScaffoldRowSelfDestructs(t *testing.T) {
	// The canonical scaffold row asserts WantErr: CodeUnimplemented.
	// Against an unimplemented stub it passes; against an implemented
	// handler it MUST fail — that failure is the contract that forces
	// scaffold rows to be rewritten with real assertions. There is no
	// permissive mode in this library: every row can fail.
	unimplemented := func(_ context.Context, _ *connect.Request[fakeReq]) (*connect.Response[fakeResp], error) {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not yet implemented"))
	}
	tdd.TableRPC(t, []tdd.Case[fakeReq, fakeResp]{
		{
			Name:    "stub satisfies the scaffold row",
			Req:     connect.NewRequest(&fakeReq{Name: "ada"}),
			WantErr: connect.CodeUnimplemented,
		},
	}, unimplemented)

	// Implemented handler: the same scaffold row must go red. TableRPC's
	// WantErr path delegates to AssertConnectError, so drive that
	// assertion directly with the implemented handler's (nil-error)
	// outcome — a zero-value testing.T cannot host t.Run subtests.
	_, err := helloHandler(context.Background(), connect.NewRequest(&fakeReq{Name: "ada"}))
	if err != nil {
		t.Fatalf("implemented handler should succeed, got %v", err)
	}
	fakeT := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(done)
		}()
		tdd.AssertConnectError(fakeT, err, connect.CodeUnimplemented)
	}()
	<-done
	if !fakeT.Failed() {
		t.Fatal("scaffold row (WantErr: CodeUnimplemented) must FAIL once the handler is implemented")
	}
}

// assertFails runs assert against a throwaway *testing.T and reports
// whether it failed. The goroutine + recover is how this file already
// drives a failing assertion (see TestTableRPC_ScaffoldRowSelfDestructs):
// t.Fatalf calls runtime.Goexit, which must not unwind the real test.
func assertFails(assert func(t *testing.T)) bool {
	fakeT := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(done)
		}()
		assert(fakeT)
	}()
	<-done
	return fakeT.Failed()
}

// TestAssertScaffoldStub_DistinguishesStubFromImplementedHandler is the
// regression this sentinel exists for.
//
// The scaffold row used to assert a bare CodeUnimplemented, which a
// FINISHED handler can also return — most commonly from a nil-guard on an
// optional dep the test harness leaves unset:
//
//	if s.deps.Store == nil { return nil, connect.NewError(connect.CodeUnimplemented, ...) }
//
// The row then passed forever against an implemented RPC. In one project
// 78 of 78 integration rows were green for exactly this reason, none of
// them asserting anything. Each case below is one of the shapes that has
// to be told apart.
func TestAssertScaffoldStub_DistinguishesStubFromImplementedHandler(t *testing.T) {
	t.Parallel()

	t.Run("forge's generated stub passes", func(t *testing.T) {
		t.Parallel()
		err := svcerr.Wrap(svcerr.ScaffoldStub("Hello"))
		if assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, err) }) {
			t.Fatal("the generated stub must satisfy the scaffold row, or a fresh scaffold fails out of the box")
		}
	})

	t.Run("nil-guard returning CodeUnimplemented fails", func(t *testing.T) {
		t.Parallel()
		err := connect.NewError(connect.CodeUnimplemented, errors.New("handler for Hello not yet implemented"))
		if !assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, err) }) {
			t.Fatal("an implemented handler's nil-guard must NOT satisfy the scaffold row — this is the bug the sentinel fixes")
		}
	})

	t.Run("deliberate svcerr.Unimplemented fails", func(t *testing.T) {
		t.Parallel()
		err := svcerr.Wrap(svcerr.Unimplemented("served by the daemonregistry forwarder"))
		if !assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, err) }) {
			t.Fatal("a deliberately-unimplemented RPC must NOT satisfy the scaffold row")
		}
	})

	t.Run("implemented handler returning success fails", func(t *testing.T) {
		t.Parallel()
		if !assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, nil) }) {
			t.Fatal("a succeeding handler must NOT satisfy the scaffold row")
		}
	})
}

// TestAssertScaffoldStub_AcceptsWireIdentification covers the integration
// tier. Through a real Connect client the error is marshalled and rebuilt,
// so the errors.Is chain is gone and only the reason metadata arrives; the
// row must still assert. Without this, unit rows and integration rows
// would mean different things under the same field name.
func TestAssertScaffoldStub_AcceptsWireIdentification(t *testing.T) {
	t.Parallel()
	rebuilt := connect.NewError(connect.CodeUnimplemented, errors.New("handler for Hello not yet implemented"))
	rebuilt.Meta().Set(svcerr.ReasonHeader, svcerr.ReasonScaffoldStub)
	if assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, rebuilt) }) {
		t.Fatal("a stub error identified by reason metadata must pass — this is the only identification that survives a Connect roundtrip")
	}
}

// TestTableRPC_WantScaffoldStubRoutes pins that the Case field actually
// reaches the assertion, rather than falling through to the WantErr path
// (WantErr's zero value is CodeCanceled, not "unset", so a mis-ordered
// branch would misassert rather than no-op).
func TestTableRPC_WantScaffoldStubRoutes(t *testing.T) {
	t.Parallel()
	stub := func(_ context.Context, _ *connect.Request[fakeReq]) (*connect.Response[fakeResp], error) {
		return nil, svcerr.Wrap(svcerr.ScaffoldStub("Hello"))
	}
	tdd.TableRPC(t, []tdd.Case[fakeReq, fakeResp]{
		{
			Name:             "stub satisfies the scaffold row",
			Req:              connect.NewRequest(&fakeReq{Name: "ada"}),
			WantScaffoldStub: true,
		},
	}, stub)

	// The red half is driven through AssertScaffoldStub rather than
	// TableRPC: TableRPC reports via t.Run, and a zero-value testing.T
	// cannot host subtests (same constraint as
	// TestTableRPC_ScaffoldRowSelfDestructs above). The routing itself is
	// what this test pins; the assertion's verdicts are pinned by
	// TestAssertScaffoldStub_DistinguishesStubFromImplementedHandler.
	_, err := helloHandler(context.Background(), connect.NewRequest(&fakeReq{Name: "ada"}))
	if err != nil {
		t.Fatalf("implemented handler should succeed, got %v", err)
	}
	if !assertFails(func(t *testing.T) { tdd.AssertScaffoldStub(t, err) }) {
		t.Fatal("WantScaffoldStub must FAIL once the handler is implemented")
	}
}

func TestAssertConnectError(t *testing.T) {
	t.Run("matching code passes", func(t *testing.T) {
		err := connect.NewError(connect.CodeNotFound, errors.New("nope"))
		// Use a sub-T so a fail in AssertConnectError doesn't fail the whole test.
		fakeT := &testing.T{}
		tdd.AssertConnectError(fakeT, err, connect.CodeNotFound)
		if fakeT.Failed() {
			t.Fatal("AssertConnectError flagged a matching error as failure")
		}
	})
	// A handler may return a bare svcerr kind: svcerr.Interceptor wraps it.
	// A test calling the *Service method directly sees the bare value, and
	// must read the code the client would (connect.CodeOf said Unknown).
	t.Run("bare svcerr kind reads its wire code", func(t *testing.T) {
		if assertFails(func(t *testing.T) { tdd.AssertConnectError(t, svcerr.NotFound("thing"), connect.CodeNotFound) }) {
			t.Fatal("a bare svcerr.NotFound must assert as CodeNotFound")
		}
		if assertFails(func(t *testing.T) { tdd.AssertConnectError(t, svcerr.ErrUnauthenticated, connect.CodeUnauthenticated) }) {
			t.Fatal("a bare sentinel must assert as its code")
		}
		if !assertFails(func(t *testing.T) { tdd.AssertConnectError(t, svcerr.NotFound("thing"), connect.CodeInternal) }) {
			t.Fatal("a mismatched code must fail")
		}
	})
	// An error nothing recognises is what the interceptor sends as Internal.
	t.Run("unclassified error reads as internal", func(t *testing.T) {
		if assertFails(func(t *testing.T) { tdd.AssertConnectError(t, errors.New("driver"), connect.CodeInternal) }) {
			t.Fatal("an unclassified error must assert as CodeInternal, the code the client reads")
		}
	})
	t.Run("nil error fails", func(t *testing.T) {
		fakeT := &testing.T{}
		// AssertConnectError calls t.Fatalf, which panics with a goexit
		// inside a subtest goroutine; capture by running in a goroutine.
		done := make(chan struct{})
		go func() {
			defer func() {
				_ = recover()
				close(done)
			}()
			tdd.AssertConnectError(fakeT, nil, connect.CodeNotFound)
		}()
		<-done
		if !fakeT.Failed() {
			t.Fatal("AssertConnectError accepted nil error")
		}
	})
}
