package tdd

import (
	"context"
	"errors"
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
// t.Run subtest that runs Setup, calls the handler with the row's request,
// and asserts WantScaffoldStub, WantErr, or no error plus Check.
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

			got, err := handler(ctx, tc.Req)

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
		})
	}
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
