package tdd_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"
)

// helloHandlerV2 is helloHandler in connect-go v2's simple shape, failing
// the three ways a v2 handler can: a v2 *connect.Error, a bare svcerr kind
// (svcerr.InterceptorV2 wraps it), and a v1 *connect.Error from svcerr.Wrap.
func helloHandlerV2(_ context.Context, req *fakeReq) (*fakeResp, error) {
	switch req.Name {
	case "":
		return nil, connectv2.NewError(connectv2.CodeInvalidArgument, "name required")
	case "ghost":
		return nil, svcerr.NotFound("person")
	case "boom":
		return nil, svcerr.Wrap(errors.New("kaboom"))
	}
	return &fakeResp{Greeting: "hi " + req.Name}, nil
}

func TestTableRPCV2_HappyAndError(t *testing.T) {
	tdd.TableRPCV2(t, []tdd.CaseV2[fakeReq, fakeResp]{
		{
			Name: "happy",
			Req:  &fakeReq{Name: "ada"},
			Check: func(t *testing.T, resp *fakeResp) {
				if resp.Greeting != "hi ada" {
					t.Fatalf("got %q, want %q", resp.Greeting, "hi ada")
				}
			},
		},
		{Name: "v2 error", Req: &fakeReq{}, WantErr: connectv2.CodeInvalidArgument},
		{Name: "bare svcerr kind", Req: &fakeReq{Name: "ghost"}, WantErr: connectv2.CodeNotFound},
		{Name: "svcerr.Wrap result", Req: &fakeReq{Name: "boom"}, WantErr: connectv2.CodeInternal},
	}, helloHandlerV2)
}

func TestTableRPCV2_SetupRunsAndCtxIsPassed(t *testing.T) {
	type key struct{}
	var setupCalls int
	var sawCtx bool
	handler := func(ctx context.Context, req *fakeReq) (*fakeResp, error) {
		sawCtx = ctx.Value(key{}) == "row"
		return helloHandlerV2(ctx, req)
	}
	tdd.TableRPCV2(t, []tdd.CaseV2[fakeReq, fakeResp]{
		{Name: "row 1", Req: &fakeReq{Name: "x"}, Setup: func(*testing.T) { setupCalls++ }},
		{Name: "row 2", Req: &fakeReq{Name: "y"}, Setup: func(*testing.T) { setupCalls++ }, Ctx: context.WithValue(context.Background(), key{}, "row")},
	}, handler)
	if setupCalls != 2 || !sawCtx {
		t.Fatalf("setup ran %d times (want 2), row ctx passed = %v", setupCalls, sawCtx)
	}
}

func TestRunRPCCasesV2_AliasMatchesTableRPCV2(t *testing.T) {
	var c tdd.CaseV2[fakeReq, fakeResp]
	var rc tdd.RPCCaseV2[fakeReq, fakeResp] = c // assigns iff alias
	_ = rc
	var ran []string
	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[fakeReq, fakeResp]{
		{Name: "happy", Req: &fakeReq{Name: "ada"}, Setup: func(*testing.T) { ran = append(ran, "happy") }},
		{Name: "missing", Req: &fakeReq{}, WantErr: connectv2.CodeInvalidArgument, Setup: func(*testing.T) { ran = append(ran, "missing") }},
	}, helloHandlerV2)
	if len(ran) != 2 || ran[0] != "happy" || ran[1] != "missing" {
		t.Fatalf("rows ran %v, want [happy missing]", ran)
	}
}

func TestAssertConnectErrorV2(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want connectv2.Code
		pass bool
	}{
		{"v2 error, matching code", connectv2.NewError(connectv2.CodeNotFound, "nope"), connectv2.CodeNotFound, true},
		{"v1 error, matching code", connect.NewError(connect.CodeNotFound, errors.New("nope")), connectv2.CodeNotFound, true},
		{"bare svcerr kind reads its wire code", svcerr.NotFound("thing"), connectv2.CodeNotFound, true},
		{"bare sentinel", svcerr.ErrUnauthenticated, connectv2.CodeUnauthenticated, true},
		{"unclassified error reads as internal", errors.New("driver"), connectv2.CodeInternal, true},
		{"mismatched code fails", svcerr.NotFound("thing"), connectv2.CodeInternal, false},
		{"nil error fails", nil, connectv2.CodeNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failed := assertFails(func(ft *testing.T) { tdd.AssertConnectErrorV2(ft, tc.err, tc.want) })
			if failed == tc.pass {
				t.Fatalf("AssertConnectErrorV2(%v, %v) failed=%v, want pass=%v", tc.err, tc.want, failed, tc.pass)
			}
		})
	}
	// The v1 assertion reads a v2 error too: svcerr.Code knows both majors.
	if assertFails(func(ft *testing.T) {
		tdd.AssertConnectError(ft, connectv2.NewError(connectv2.CodeAborted, "x"), connect.CodeAborted)
	}) {
		t.Fatal("AssertConnectError must read a v2 *connect.Error's code")
	}
}

const stubProcedure = "/tdd.v1.Svc/Hello"

// callStubV2 serves handlerErr from a connect-go v2 server behind
// svcerr.InterceptorV2 and calls it with a v2 client, returning what the
// client got: the error and the call's CallInfo.
func callStubV2(t *testing.T, handlerErr error) (error, *connectv2.CallInfo) {
	t.Helper()
	spec := connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: stubProcedure}
	server := connectv2.NewServer(svcerr.InterceptorV2())
	server.Register(connectv2.Method{Spec: spec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req emptypb.Empty
		if err := stream.Receive(&req); err != nil {
			return err
		}
		if handlerErr != nil {
			return handlerErr
		}
		return stream.Send(&emptypb.Empty{})
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	ctx, info := connectv2.NewClientContext(context.Background())
	err := client.CallUnary(ctx, spec, &emptypb.Empty{}, &emptypb.Empty{})
	return err, info
}

// TestAssertScaffoldStubV2_OverTheWire is the v2 integration tier: through a
// v2 client the errors.Is chain is gone and the reason arrives on the
// CallInfo, not the error. The stub must still pass, and every lookalike
// must still fail.
func TestAssertScaffoldStubV2_OverTheWire(t *testing.T) {
	err, info := callStubV2(t, svcerr.ScaffoldStub("Hello"))
	if errors.Is(err, svcerr.ErrScaffoldStub) {
		t.Fatal("precondition: the errors.Is chain should not survive the wire")
	}
	if assertFails(func(ft *testing.T) { tdd.AssertScaffoldStubV2(ft, err, info) }) {
		t.Fatalf("forge's stub served over v2 must satisfy the row (err=%v, header=%v)", err, info.ResponseHeader().Get(svcerr.ReasonHeader))
	}
	if !assertFails(func(ft *testing.T) { tdd.AssertScaffoldStubV2(ft, err, nil) }) {
		t.Fatal("without the CallInfo a wire error cannot be identified and must fail, not pass vacuously")
	}

	for name, handlerErr := range map[string]error{
		"deliberate svcerr.Unimplemented": svcerr.Unimplemented("served by a forwarder"),
		"nil-guard v2 unimplemented":      connectv2.NewError(connectv2.CodeUnimplemented, "handler for Hello not yet implemented"),
		"implemented handler":             nil,
	} {
		t.Run(name, func(t *testing.T) {
			err, info := callStubV2(t, handlerErr)
			if !assertFails(func(ft *testing.T) { tdd.AssertScaffoldStubV2(ft, err, info) }) {
				t.Fatalf("%s must NOT satisfy the scaffold row", name)
			}
		})
	}
}

func TestAssertScaffoldStubV2_InProcess(t *testing.T) {
	if assertFails(func(ft *testing.T) { tdd.AssertScaffoldStubV2(ft, svcerr.ScaffoldStub("Hello"), nil) }) {
		t.Fatal("the stub returned in process must satisfy the row by errors.Is")
	}
	if assertFails(func(ft *testing.T) {
		tdd.AssertScaffoldStubV2(ft, svcerr.ToConnectV2(svcerr.ScaffoldStub("Hello")), nil)
	}) {
		t.Fatal("the stub converted by ToConnectV2 keeps its identity in process")
	}
	tdd.TableRPCV2(t, []tdd.CaseV2[fakeReq, fakeResp]{
		{Name: "stub satisfies the scaffold row", Req: &fakeReq{Name: "ada"}, WantScaffoldStub: true},
	}, func(context.Context, *fakeReq) (*fakeResp, error) { return nil, svcerr.ScaffoldStub("Hello") })
}

// tenantHandlerV2 reads and writes call metadata the way
// connect-go-v2-migrate rewrites a handler to: through the server CallInfo,
// with no nil check. Called directly it would panic; through the v2 helpers
// it must not.
func tenantHandlerV2(ctx context.Context, req *fakeReq) (*fakeResp, error) {
	info, _ := connectv2.CallInfoForServerContext(ctx)
	tenant := info.RequestHeader().Get("X-Tenant")
	if tenant == "" {
		return nil, connectv2.NewError(connectv2.CodeUnauthenticated, "tenant header required")
	}
	info.ResponseHeader().Set("X-Served-Tenant", tenant)
	info.ResponseTrailer().Set("X-Procedure", info.Spec.Procedure)
	return &fakeResp{Greeting: "hi " + req.Name + " of " + tenant}, nil
}

func TestTableRPCV2_HandlerSeesCallInfoAndRowHeader(t *testing.T) {
	var checkedInfo bool
	tdd.TableRPCV2(t, []tdd.CaseV2[fakeReq, fakeResp]{
		{
			Name:   "row header reaches the handler",
			Req:    &fakeReq{Name: "ada"},
			Header: http.Header{"x-tenant": {"acme"}}, // canonicalized on the way in
			Check: func(t *testing.T, resp *fakeResp) {
				if resp.Greeting != "hi ada of acme" {
					t.Fatalf("got %q, want %q", resp.Greeting, "hi ada of acme")
				}
			},
			CheckCallInfo: func(t *testing.T, info *connectv2.CallInfo) {
				checkedInfo = true
				if got := info.ResponseHeader().Get("X-Served-Tenant"); got != "acme" {
					t.Fatalf("response header X-Served-Tenant = %q, want acme", got)
				}
				if got := info.ResponseTrailer().Get("X-Procedure"); got == "" {
					t.Fatal("the handler saw no procedure on its CallInfo")
				}
			},
		},
		{Name: "no header", Req: &fakeReq{Name: "ada"}, WantErr: connectv2.CodeUnauthenticated},
	}, tenantHandlerV2)
	if !checkedInfo {
		t.Fatal("CheckCallInfo did not run on the successful row")
	}
}

func TestCallV2(t *testing.T) {
	t.Run("handler gets a server CallInfo and the caller's request", func(t *testing.T) {
		req := &fakeReq{Name: "ada"}
		var sawInfo, sameReq bool
		resp, err := tdd.CallV2(context.Background(), func(ctx context.Context, got *fakeReq) (*fakeResp, error) {
			_, sawInfo = connectv2.CallInfoForServerContext(ctx)
			sameReq = got == req
			return &fakeResp{Greeting: "ok"}, nil
		}, req, nil)
		if err != nil || resp == nil || resp.Greeting != "ok" {
			t.Fatalf("CallV2 = %v, %v; want the handler's response", resp, err)
		}
		if !sawInfo || !sameReq {
			t.Fatalf("handler saw CallInfo = %v, the caller's request = %v; want both", sawInfo, sameReq)
		}
	})

	t.Run("info carries headers both ways and names the procedure", func(t *testing.T) {
		info := &connectv2.CallInfo{}
		info.Spec.Procedure = "/acme.v1.Svc/Hello"
		info.RequestHeader().Set("X-Tenant", "acme")
		if _, err := tdd.CallV2(context.Background(), tenantHandlerV2, &fakeReq{Name: "ada"}, info); err != nil {
			t.Fatalf("CallV2: %v", err)
		}
		if got := info.ResponseHeader().Get("X-Served-Tenant"); got != "acme" {
			t.Fatalf("response header = %q, want acme", got)
		}
		if got := info.ResponseTrailer().Get("X-Procedure"); got != "/acme.v1.Svc/Hello" {
			t.Fatalf("handler saw procedure %q, want the one info named", got)
		}
	})

	t.Run("the handler's error comes back unconverted", func(t *testing.T) {
		for name, handlerErr := range map[string]error{
			"scaffold stub":    svcerr.ScaffoldStub("Hello"),
			"bare svcerr kind": svcerr.NotFound("person"),
			"plain error":      errors.New("driver"),
		} {
			t.Run(name, func(t *testing.T) {
				resp, err := tdd.CallV2(context.Background(), func(context.Context, *fakeReq) (*fakeResp, error) {
					return &fakeResp{}, handlerErr
				}, &fakeReq{}, nil)
				if resp != nil {
					t.Fatalf("response on error = %v, want nil", resp)
				}
				if !errors.Is(err, handlerErr) {
					t.Fatalf("err = %v, want the handler's own error", err)
				}
			})
		}
	})

	t.Run("nil request and nil response pass through", func(t *testing.T) {
		resp, err := tdd.CallV2(context.Background(), func(_ context.Context, got *fakeReq) (*fakeResp, error) {
			if got != nil {
				t.Errorf("request = %v, want nil", got)
			}
			return nil, nil
		}, nil, nil)
		if resp != nil || err != nil {
			t.Fatalf("CallV2 = %v, %v; want nil, nil", resp, err)
		}
	})
}
