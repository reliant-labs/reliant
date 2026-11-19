package tdd

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Case is a table-driven test row for a Connect RPC.
//
// Req is the request proto type and Resp is the response proto type;
// Forge handlers receive *connect.Request[Req] and return
// *connect.Response[Resp], which matches the signature the helper expects.
//
// Either WantErr or Check should be set — never both. WantErr asserts the
// handler returned a Connect error with the given code; Check is a
// caller-supplied function for asserting on the response (typically used
// for happy-path cases). If neither is set, the helper only verifies
// that the call did not return an error.
//
// There is deliberately no "tolerate any outcome" mode: every row must be
// able to fail. Scaffold rows for not-yet-implemented handlers set
// WantScaffoldStub — such a row self-destructs (goes red) the moment the
// handler is implemented, forcing it to be rewritten with a real Check /
// WantErr assertion.
type Case[Req, Resp any] struct {
	// Name identifies the row; passed straight to t.Run.
	Name string

	// Req is the inbound *connect.Request used in the call.
	Req *connect.Request[Req]

	// WantErr, if non-zero, asserts the handler returned an error the
	// client reads with this code (svcerr.Code: a *connect.Error, or a bare
	// svcerr kind the interceptor wraps). The Check function is not
	// consulted in this case.
	WantErr connect.Code

	// WantScaffoldStub asserts the handler is still forge's generated
	// stub — i.e. nobody has implemented this RPC yet. It is what the
	// scaffold row forge emits alongside each stub sets, and it is
	// designed to STOP passing: implement the handler and the row fails,
	// which is the prompt to replace it with a real assertion.
	//
	// Do not reach for this in a hand-written row. It asserts an absence
	// of implementation, so it can only ever be true of code nobody has
	// written; to assert that a finished handler answers Unimplemented
	// (a forwarder, a feature-flagged path), use
	// WantErr: connect.CodeUnimplemented, which this deliberately does
	// not match.
	WantScaffoldStub bool

	// Check is invoked on a successful (err == nil) response so the test
	// can assert on the returned message. Optional.
	Check func(t *testing.T, resp *connect.Response[Resp])

	// Setup runs before the handler is invoked. Use it to seed mocks or
	// populate per-row state. Cleanup should be registered via t.Cleanup
	// inside the closure.
	Setup func(t *testing.T)

	// Ctx, if non-nil, overrides the default context.Background() passed
	// to the handler. Use [WithTimeout] for a deadlined context.
	Ctx context.Context
}

// HandlerFunc is the Connect RPC handler signature TableRPC drives.
type HandlerFunc[Req, Resp any] func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error)

// TableRPC runs a slice of [Case] rows against a Connect handler.
// Each row becomes a t.Run subtest. The helper:
//   - calls Setup if present,
//   - invokes the handler with the row's request,
//   - asserts on WantErr (if set) or runs Check (if the call succeeded).
//
// Subtests run with t.Parallel disabled by default so per-row Setup that
// touches shared state stays correct. Wrap the call site in a parallel
// outer test if you want concurrency; per-row parallelism is the caller's
// choice and not the library's default.
func TableRPC[Req, Resp any](
	t *testing.T,
	cases []Case[Req, Resp],
	handler HandlerFunc[Req, Resp],
) {
	t.Helper()
	for _, tc := range cases {
		tc := tc
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
				AssertConnectError(t, err, tc.WantErr)
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

// RPCCase is a type alias for [Case] — used by the codegen-emitted
// `handlers_crud_test_gen.go` shim so the generated identifier matches
// the migration skill's documentation. Hand-written tests may use
// either name; they are the same type.
type RPCCase[Req, Resp any] = Case[Req, Resp]

// RunRPCCases is a function alias for [TableRPC] — see [RPCCase]. The
// codegen-emitted test shim uses RunRPCCases so the generated call
// site mirrors the migration skill's documentation. Hand-written
// tests may call either; they have identical behaviour.
func RunRPCCases[Req, Resp any](
	t *testing.T,
	cases []RPCCase[Req, Resp],
	handler HandlerFunc[Req, Resp],
) {
	t.Helper()
	TableRPC(t, cases, handler)
}

// AssertScaffoldStub asserts err is the error forge's generated handler
// stub returns — svcerr.ErrScaffoldStub. It backs Case.WantScaffoldStub
// and is exported for hand-written tests that drive a stub directly.
//
// It accepts either identification, because which one is available
// depends on how far the error travelled. In process — a row calling the
// handler method directly — the chain is intact and errors.Is matches.
// Through a real Connect client the chain does not survive: the error is
// marshalled to the wire and rebuilt on the far side, so errors.Is is
// false and the reason METADATA is what arrives. Integration rows go over
// a client and unit rows do not, so both paths must work or the assertion
// would silently mean different things at the two tiers.
//
// The failure message names what it found, because the interesting case
// is not "no error" but "a DIFFERENT error" — that is what an
// implemented handler produces, and the whole point of this row.
func AssertScaffoldStub(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the generated scaffold stub error, got nil — this RPC looks implemented, so replace this row with a real assertion")
	}
	if errors.Is(err, svcerr.ErrScaffoldStub) {
		return
	}
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Meta().Get(svcerr.ReasonHeader) == svcerr.ReasonScaffoldStub {
		return
	}
	t.Fatalf("expected the generated scaffold stub error, got %v — if you implemented this RPC, replace this row with a real Check / WantErr assertion", err)
}

// AssertConnectError asserts that err is non-nil and that the client would
// read the given code for it. It is the canonical assertion helper used
// inside TableRPC and is exported for direct use in hand-written tests.
//
// "Would read" is svcerr.Code: the code svcerr.Interceptor sends for err.
// A handler test calls the *Service method directly, with no interceptor
// in between, so the error it sees is whatever the handler returned — and a
// handler may return a bare svcerr kind (`return nil, svcerr.NotFound("x")`)
// because the interceptor wraps it. connect.CodeOf read that as Unknown,
// which made the documented handler shape untestable without a client. For
// a *connect.Error the two agree.
func AssertConnectError(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected connect error with code %s, got nil", want)
	}
	if got := svcerr.Code(err); got != want {
		t.Fatalf("expected connect code %s, got %s (err=%v)", want, got, err)
	}
}
