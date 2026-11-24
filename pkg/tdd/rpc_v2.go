package tdd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	connectv2 "connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The connect-go v2 forms of the RPC table helpers.
//
// v2 handlers are "simple": a generated v2 handler method is
// func(ctx, *Req) (*Resp, error), with no Request/Response wrappers. Go
// cannot overload a generic type, so the v2 row and runner are their own
// symbols, named by the package's rule for the connect-go v2 surface: the v1
// symbol plus V2. Each behaves exactly as its v1 twin; only the message
// shapes and the code type differ. ContractCase / TableContract, E2EClient
// and NewMock have no connect type and serve both majors unchanged.
//
// One thing v2 moves: request and response metadata live on the call's
// *connect.CallInfo, which a handler reaches through
// connectv2.CallInfoForServerContext — and which is nil when a test calls
// the handler method directly. So the v2 helpers never call the handler
// directly: each call goes through [CallV2], an in-process connect.Server
// with no interceptors, which gives the handler a real CallInfo and returns
// its error exactly as the handler returned it.

// CaseV2 is [Case] for a connect-go v2 handler: Req is the bare request
// message and Check receives the bare response. Every field means what it
// means on Case — Either WantErr or Check, never both; WantScaffoldStub for
// the rows forge scaffolds beside a stub, which go red once it is
// implemented.
type CaseV2[Req, Resp any] struct {
	// Name identifies the row; passed straight to t.Run.
	Name string

	// Req is the request message passed to the handler.
	Req *Req

	// WantErr, if non-zero, asserts the handler returned an error the
	// client reads with this code ([AssertConnectErrorV2]: a *connect.Error
	// of either major, or a bare svcerr kind the interceptor wraps). Check
	// is not consulted in this case.
	WantErr connectv2.Code

	// WantScaffoldStub asserts the handler is still forge's generated stub;
	// see [Case.WantScaffoldStub].
	WantScaffoldStub bool

	// Check is invoked on a successful (err == nil) response. Optional.
	Check func(t *testing.T, resp *Resp)

	// Header is the request header the handler reads from its CallInfo
	// (connectv2.CallInfoForServerContext(ctx).RequestHeader()) — what a
	// v1 row set on Req.Header(). Optional; keys are canonicalized.
	Header http.Header

	// CheckCallInfo is invoked after Check on a successful response with
	// the call's CallInfo, to assert the response headers and trailers the
	// handler set — what a v1 Check read from resp.Header() and
	// resp.Trailer(). Optional.
	CheckCallInfo func(t *testing.T, info *connectv2.CallInfo)

	// Setup runs before the handler is invoked. Optional.
	Setup func(t *testing.T)

	// Ctx, if non-nil, overrides the default context.Background().
	Ctx context.Context
}

// HandlerFuncV2 is the connect-go v2 handler signature [TableRPCV2] drives:
// a generated v2 handler method, func(ctx, *Req) (*Resp, error). A method
// value (svc.GetUser) converts to it implicitly.
type HandlerFuncV2[Req, Resp any] func(context.Context, *Req) (*Resp, error)

// TableRPCV2 is [TableRPC] for a connect-go v2 handler: each row becomes a
// t.Run subtest that runs Setup, calls the handler with the row's request
// and Header through [CallV2], and asserts WantScaffoldStub, WantErr, or no
// error plus Check and CheckCallInfo.
func TableRPCV2[Req, Resp any](
	t *testing.T,
	cases []CaseV2[Req, Resp],
	handler HandlerFuncV2[Req, Resp],
) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Setup != nil {
				tc.Setup(t)
			}

			ctx := tc.Ctx
			if ctx == nil {
				ctx = context.Background()
			}

			info := &connectv2.CallInfo{}
			for key, values := range tc.Header {
				for _, value := range values {
					info.RequestHeader().Add(key, value)
				}
			}

			got, err := CallV2(ctx, handler, tc.Req, info)

			if tc.WantScaffoldStub {
				AssertScaffoldStub(t, err)
				return
			}

			if tc.WantErr != 0 {
				AssertConnectErrorV2(t, err, tc.WantErr)
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.Check != nil {
				tc.Check(t, got)
			}
			if tc.CheckCallInfo != nil {
				tc.CheckCallInfo(t, info)
			}
		})
	}
}

// inProcessProcedureV2 is the procedure [CallV2] serves a handler under
// when the CallInfo it is given names none.
const inProcessProcedureV2 = "/forge.tdd.v2.InProcess/Call"

// CallV2 calls a connect-go v2 handler the way a server does, in process:
// it registers handler as a unary method on a fresh connect.Server with no
// interceptors and serves one call to it through [connect.Server.Call]
// with info as the call's CallInfo. The handler therefore sees a real
// server CallInfo — connectv2.CallInfoForServerContext(ctx) succeeds — and
// receives req itself (the same pointer, as a direct call would), read
// through an in-memory unary ServerStream.
//
// Use it in a hand-written test of a handler that reads or writes call
// metadata; [TableRPCV2] rows use it for every call. Set request headers on
// info before the call and read the response headers and trailers the
// handler set from it afterwards:
//
//	info := &connectv2.CallInfo{}
//	info.RequestHeader().Set("X-Tenant", "acme")
//	resp, err := tdd.CallV2(ctx, svc.GetOrder, &pb.GetOrderRequest{Id: "o1"}, info)
//	_ = info.ResponseHeader().Get("X-Request-Cost")
//
// info may be nil when the test needs neither. The call is served under
// info.Spec.Procedure, or "/forge.tdd.v2.InProcess/Call" when that is empty, and
// info.Spec.StreamType is unary. ctx may be nil (context.Background()).
//
// The error is the handler's own, unconverted: no svcerr interceptor runs,
// as none ran when a v1 test called its handler directly, so errors.Is
// still sees the handler's chain and [AssertConnectErrorV2] reads the code
// the production chain would send. On error the response is nil, as a
// client would see it.
func CallV2[Req, Resp any](ctx context.Context, handler HandlerFuncV2[Req, Resp], req *Req, info *connectv2.CallInfo) (*Resp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info == nil {
		info = &connectv2.CallInfo{}
	}
	if info.Spec.Procedure == "" {
		info.Spec.Procedure = inProcessProcedureV2
	}
	info.Spec.StreamType = connectv2.StreamTypeUnary

	server := connectv2.NewServer()
	server.Register(connectv2.Method{
		Spec: info.Spec,
		// The adapter generated code registers for a simple unary method:
		// receive the request, call the handler, send its response.
		Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			msg := req
			if err := stream.Receive(msg); err != nil {
				return err
			}
			resp, err := handler(ctx, msg)
			if err != nil {
				return err
			}
			return stream.Send(resp)
		},
	})
	stream := &unaryStreamV2[Req, Resp]{req: req}
	if err := server.Call(ctx, info.Spec.Procedure, info, stream); err != nil {
		return nil, err
	}
	return stream.resp, nil
}

// unaryStreamV2 is the in-memory connect.ServerStream [CallV2] serves a
// unary call over: one request, which it hands over by pointer instead of
// copying (the handler gets the caller's message, as in a direct call), and
// one response.
type unaryStreamV2[Req, Resp any] struct {
	req      *Req
	received bool
	resp     *Resp
	sent     bool
}

func (s *unaryStreamV2[Req, Resp]) Receive(msg any) error {
	if s.received {
		return io.EOF
	}
	if m, ok := msg.(*Req); !ok || m != s.req {
		return connectv2.Errorf(connectv2.CodeInternal, "tdd: in-process stream receives only the call's own %T, got %T", s.req, msg)
	}
	s.received = true
	return nil
}

func (s *unaryStreamV2[Req, Resp]) SendHeaders() error { return nil }

func (s *unaryStreamV2[Req, Resp]) Send(msg any) error {
	if s.sent {
		return connectv2.NewError(connectv2.CodeInternal, "tdd: a unary handler sent a second response")
	}
	resp, ok := msg.(*Resp)
	if !ok {
		return connectv2.Errorf(connectv2.CodeInternal, "tdd: in-process stream sends %T, got %T", s.resp, msg)
	}
	s.resp, s.sent = resp, true
	return nil
}

// RPCCaseV2 is a type alias for [CaseV2], as [RPCCase] is for Case: the
// identifier forge's generated v2 test shims use.
type RPCCaseV2[Req, Resp any] = CaseV2[Req, Resp]

// RunRPCCasesV2 is [TableRPCV2] under the name forge's generated v2 test
// shims call, as [RunRPCCases] is for TableRPC.
func RunRPCCasesV2[Req, Resp any](
	t *testing.T,
	cases []RPCCaseV2[Req, Resp],
	handler HandlerFuncV2[Req, Resp],
) {
	t.Helper()
	TableRPCV2(t, cases, handler)
}

// AssertConnectErrorV2 is [AssertConnectError] with a connect-go v2 code:
// err must be non-nil and the client must read want for it. "Would read" is
// svcerr.CodeV2 — the code svcerr.InterceptorV2 sends — so a bare svcerr
// kind, a v1 *connect.Error (what svcerr.Wrap and crud return) and a v2
// *connect.Error all assert by the code they put on the wire.
// connectv2.CodeOf would read the first two as Unknown.
func AssertConnectErrorV2(t *testing.T, err error, want connectv2.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected connect error with code %s, got nil", want)
	}
	if got := svcerr.CodeV2(err); got != want {
		t.Fatalf("expected connect code %s, got %s (err=%v)", want, got, err)
	}
}

// AssertScaffoldStubV2 is [AssertScaffoldStub] for a call made through a
// connect-go v2 client. A v2 error carries no metadata: the reason that
// identifies forge's stub across the wire arrives on the call's
// *connect.CallInfo instead — the response header for a unary Connect call,
// the trailer otherwise — so pass the CallInfo the call used
// (connectv2.NewClientContext). In process, with no client, info may be nil;
// errors.Is identifies the stub there, as it does for AssertScaffoldStub.
func AssertScaffoldStubV2(t *testing.T, err error, info *connectv2.CallInfo) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the generated scaffold stub error, got nil — this RPC looks implemented, so replace this row with a real assertion")
	}
	if errors.Is(err, svcerr.ErrScaffoldStub) {
		return
	}
	if info != nil && svcerr.CodeV2(err) == connectv2.CodeUnimplemented {
		for _, h := range []*connectv2.Header{info.ResponseHeader(), info.ResponseTrailer()} {
			if h.Get(svcerr.ReasonHeader) == svcerr.ReasonScaffoldStub {
				return
			}
		}
	}
	t.Fatalf("expected the generated scaffold stub error, got %v — if you implemented this RPC, replace this row with a real Check / WantErr assertion", err)
}
