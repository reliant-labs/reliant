package testkit

// Request validation on the test server — warn first, then enforce
// (forge#2283).
//
// Production ends every handler chain with protovalidate → svcerr
// (serverkit.Chain): a request that breaks its proto's buf.validate rules is
// answered invalid_argument before the handler runs. NewConnectClient's test
// server had only svcerr, so a test could send a request production rejects,
// see the handler run, and pass. The rules were invisible to every
// scaffolded test tier.
//
// Turning validation on outright would change what existing tests see: a
// test that sends an invalid request — usually to exercise the handler's own
// check of the same thing — would start reading invalid_argument from the
// interceptor instead. So it rolls out the way a new lint rule does:
//
//   - now: NewConnectClient validates every request and LOGS each violation
//     on the test (t.Log, shown by `go test -v` and on failure) with what
//     production does about it, and lets the request through unchanged;
//   - opt in to production's behaviour today with ValidateRequests();
//   - a later forge release makes enforcement the default.

import (
	"context"
	"sync"
	"testing"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/validate"
)

// ValidateRequests returns a handler option that rejects a request breaking
// its protovalidate rules with invalid_argument before the handler runs,
// exactly as production's chain does. Pass it to NewConnectClient (or to a
// generated NewTest<Svc>Server's handler options) to test with production's
// behaviour now, ahead of the release that makes it the default:
//
//	srv, client := testkit.NewConnectClient(t, svc.Register,
//	    orderv1connect.NewOrderServiceClient, testkit.ValidateRequests())
//
// It panics if protovalidate cannot build a validator, which only a broken
// build of the protovalidate module does.
func ValidateRequests() connect.HandlerOption {
	ic, err := validate.Interceptor()
	if err != nil {
		panic("testkit: build the protovalidate interceptor: " + err.Error())
	}
	return connect.WithInterceptors(ic)
}

// requestValidator is the validator the warning interceptor uses; a variable
// so the package's own tests can supply one without a proto that carries
// rules.
var requestValidator = sync.OnceValues(func() (protovalidate.Validator, error) {
	return protovalidate.New()
})

// warnInvalidRequests is the interceptor NewConnectClient installs: it
// validates what production validates and logs on t, never rejects.
type warnInvalidRequests struct {
	t testing.TB
	v protovalidate.Validator
}

// newWarnInvalidRequests returns the warning interceptor for t, or nil when
// no validator can be built (the test server then behaves as it always did).
func newWarnInvalidRequests(t testing.TB) connect.Interceptor {
	v, err := requestValidator()
	if err != nil || v == nil {
		return nil
	}
	return &warnInvalidRequests{t: t, v: v}
}

func (w *warnInvalidRequests) check(procedure string, msg any) {
	m, ok := msg.(proto.Message)
	if !ok {
		return
	}
	if err := w.v.Validate(m); err != nil {
		w.t.Logf("testkit: %s request breaks its protovalidate rules (%v). Production rejects it with "+
			"invalid_argument before the handler runs; this test server let it through and only warns, and a "+
			"later forge release rejects it too. Pass testkit.ValidateRequests() to test with production's "+
			"behaviour now (forge#2283).", procedure, err)
	}
}

// WrapUnary checks the request, then calls the handler whatever the result.
func (w *warnInvalidRequests) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		w.check(req.Spec().Procedure, req.Any())
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op: validation is a handler concern.
func (w *warnInvalidRequests) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler checks every message the handler receives.
func (w *warnInvalidRequests) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, &warningConn{StreamingHandlerConn: conn, w: w})
	}
}

type warningConn struct {
	connect.StreamingHandlerConn
	w *warnInvalidRequests
}

func (c *warningConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	c.w.check(c.Spec().Procedure, msg)
	return nil
}
