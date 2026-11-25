package testkit

// connect_v2.go — the over-the-wire test harness for connect-go v2 services.
//
// Each symbol is its v1 twin with v2 types: interceptors are
// connectv2.ServerInterceptor (v2 has no handler options), a service
// registers on a *connect.Server, and a client is built from a
// *connect.Client. Behaviour — the principal, request validation, the
// warning rollout and svcerr innermost — is the v1 harness's.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/validate"
)

// ServerPrincipalV2 is ServerPrincipal for a connect-go v2 test server: an
// interceptor that installs a test principal in the handler's context on
// every call — unary and streaming alike — through the project's own claims
// setter. Defaults are AuthedContext's.
//
//	testkit.NewConnectClientV2(t, svc.RegisterV2, orderv1connect.NewOrderServiceClient,
//	    testkit.ServerPrincipalV2(middleware.ContextWithClaims, testkit.WithRoles("viewer")))
func ServerPrincipalV2(withClaims func(context.Context, *auth.Claims) context.Context, opts ...ClaimsOption) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			// Rebuilt per call, so a handler mutating its principal cannot
			// leak the change into the next call.
			claims := defaultTestClaims()
			for _, opt := range opts {
				opt(claims)
			}
			return next(withClaims(ctx, claims), spec, stream)
		}
	}
}

// ValidateRequestsV2 is ValidateRequests for a connect-go v2 test server: an
// interceptor rejecting a request that breaks its protovalidate rules with
// invalid_argument before the handler runs, as production's chain does. It
// panics if protovalidate cannot build a validator.
func ValidateRequestsV2() connectv2.ServerInterceptor {
	ic, err := validate.InterceptorV2()
	if err != nil {
		panic("testkit: build the protovalidate interceptor: " + err.Error())
	}
	return ic
}

// NewConnectClientV2 is NewConnectClient for a connect-go v2 service: it
// starts a test server with register's service on it and builds a typed
// client against it with newClient — the v2 generated
// New<Svc>Client(*connect.Client):
//
//	srv, client := testkit.NewConnectClientV2(t,
//	    svc.RegisterV2, orderv1connect.NewOrderServiceClient)
//
// The server's chain is interceptors (e.g. ServerPrincipalV2), then the
// request-validation warning (validate.go), then svcerr.InterceptorV2,
// innermost — NewConnectClient's chain. It is mounted with mountkit.MountV2, so
// an unknown method of the service is a plain 404, as in production and in
// v1. Server and client both read messages of any size, as v1's do (v2's
// default is 4 MiB). The client's HTTP client is
// traced, so a test's own span is the parent of the server's. The client runs
// svcerr.ClientInterceptorV2, so a test reads the reason a call failed with
// as svcerr.MetaOf(err).Get(svcerr.ReasonHeader) — v1's ce.Meta().
func NewConnectClientV2[C any](
	t *testing.T,
	register func(server *connectv2.Server),
	newClient func(client *connectv2.Client) C,
	interceptors ...connectv2.ServerInterceptor,
) (*httptest.Server, C) {
	t.Helper()
	ints := make([]connectv2.ServerInterceptor, 0, len(interceptors)+2)
	for _, ic := range interceptors {
		if ic != nil {
			ints = append(ints, ic)
		}
	}
	if warn := newWarnInvalidRequestsV2(t); warn != nil {
		ints = append(ints, warn)
	}
	server := connectv2.NewServer(svcerr.WithInterceptorV2(ints)...)
	register(server)
	srv := NewTestServer(t, func(mux *http.ServeMux) {
		mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	})
	transport := connecthttp.NewTransport(observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: -1}), srv.URL,
		connecthttp.WithReadMaxBytes(0))
	return srv, newClient(connectv2.NewClient(transport, svcerr.ClientInterceptorV2()))
}

// newWarnInvalidRequestsV2 is newWarnInvalidRequests' v2 interceptor: it
// checks every received request message and logs a violation on t, never
// rejects. nil when no validator can be built.
func newWarnInvalidRequestsV2(t testing.TB) connectv2.ServerInterceptor {
	v, err := requestValidator()
	if err != nil || v == nil {
		return nil
	}
	w := &warnInvalidRequests{t: t, v: v, optIn: "testkit.ValidateRequestsV2()"}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(ctx, spec, &warningStreamV2{ServerStream: stream, w: w, procedure: spec.Procedure})
		}
	}
}

type warningStreamV2 struct {
	connectv2.ServerStream
	w         *warnInvalidRequests
	procedure string
}

func (s *warningStreamV2) Receive(msg any) error {
	if err := s.ServerStream.Receive(msg); err != nil {
		return err
	}
	s.w.check(s.procedure, msg)
	return nil
}
