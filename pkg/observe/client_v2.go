// File: client_v2.go — the outbound client stack for connect-go v2.
//
// A v2 generated client constructor takes one *connect.Client
// (`foov1connect.NewFooServiceClient(client)`), built from a transport and the
// client interceptors, so the stack hands over those parts instead of v1's
// HTTP client plus ClientOptions:
//
//	stack, err := observe.NewClientStackV2(observe.ClientStackDepsV2{})
//	if err != nil { ... }
//	infra.Users = usersv1connect.NewUsersServiceClient(stack.NewClient(cfg.UsersBaseURL))
//
// It bundles what NewClientStack bundles — the otelconnect client interceptor,
// request-id forwarding, a default timeout — plus two v2 differences. connect-go
// v2 caps a client's response messages at 4 MiB by default, where v1 read any
// size, so the stack lifts the cap (connecthttp.WithReadMaxBytes(0)) unless the
// caller sets one. And a v2 error carries no metadata, where v1's Meta() held
// the response headers and trailers, so the stack installs
// svcerr.ClientInterceptorV2 first: svcerr.MetaOf(err) then reads the reason
// (svcerr.ReasonHeader) and metadata a server sent with a failed call.

package observe

import (
	"context"
	"fmt"
	"net/http"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/otelconnect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ClientStackDepsV2 configures NewClientStackV2. The zero value is the
// production default, as ClientStackDeps's.
type ClientStackDepsV2 struct {
	// Timeout bounds each outbound request (http.Client.Timeout), as
	// ClientStackDeps.Timeout: zero is 30s, negative is none (long-lived
	// server streams need none).
	Timeout time.Duration

	// Transport is the base RoundTripper, as ClientStackDeps.Transport. nil
	// is http.DefaultTransport. Do not wrap it in otelhttp.
	Transport http.RoundTripper

	// Extras are appended after the canonical client interceptors
	// (svcerr error metadata, otelconnect, request-id forwarding), in the
	// order supplied — e.g.
	// auth-token injection. nil entries are dropped.
	Extras []connectv2.ClientInterceptor

	// HTTPOptions are transport options (protocol, codec, compression,
	// limits), applied after the stack's own unbounded read limit, so a
	// connecthttp.WithReadMaxBytes here sets the limit.
	HTTPOptions []connecthttp.Option
}

// ClientStackV2 is what a connect-go v2 generated client is built from. Use
// NewClient, or assemble the *connectv2.Client from the parts.
type ClientStackV2 struct {
	// HTTPClient carries the timeout and the base transport. It is fresh per
	// stack, so a caller may tweak it without affecting other stacks.
	HTTPClient *http.Client

	// Interceptors is the client interceptor chain, for connectv2.NewClient:
	// svcerr.ClientInterceptorV2, otelconnect, request-id forwarding, then
	// ClientStackDepsV2.Extras.
	Interceptors []connectv2.ClientInterceptor

	// HTTPOptions are the transport options, for connecthttp.NewTransport.
	HTTPOptions []connecthttp.Option
}

// NewClient returns the *connectv2.Client for baseURL over this stack — the
// argument a v2 generated New<Svc>Client takes.
func (s ClientStackV2) NewClient(baseURL string) *connectv2.Client {
	return connectv2.NewClient(connecthttp.NewTransport(s.HTTPClient, baseURL, s.HTTPOptions...), s.Interceptors...)
}

// NewClientStackV2 is NewClientStack for connect-go v2 clients. Its error is
// the otelconnect client interceptor's construction error.
func NewClientStackV2(deps ClientStackDepsV2) (ClientStackV2, error) {
	timeout := deps.Timeout
	switch {
	case timeout == 0:
		timeout = defaultClientTimeout
	case timeout < 0:
		timeout = 0
	}

	otelInt, err := otelconnect.NewClientInterceptor()
	if err != nil {
		return ClientStackV2{}, fmt.Errorf("otelconnect client interceptor: %w", err)
	}

	chain := make([]connectv2.ClientInterceptor, 0, 3+len(deps.Extras))
	chain = append(chain, svcerr.ClientInterceptorV2(), otelInt, ClientRequestIDInterceptorV2())
	for _, ic := range deps.Extras {
		if ic != nil {
			chain = append(chain, ic)
		}
	}

	opts := make([]connecthttp.Option, 0, 1+len(deps.HTTPOptions))
	opts = append(opts, connecthttp.WithReadMaxBytes(0))
	opts = append(opts, deps.HTTPOptions...)

	return ClientStackV2{
		HTTPClient: &http.Client{
			Transport: deps.Transport,
			Timeout:   timeout,
		},
		Interceptors: chain,
		HTTPOptions:  opts,
	}, nil
}

// ClientRequestIDInterceptorV2 is ClientRequestIDInterceptor for a connect-go
// v2 client: it forwards the correlation id on ctx (RequestIDFromContext) as
// the outgoing RequestIDHeader, on unary and streaming calls alike, unless the
// call already sets one. A ctx without an id forwards nothing.
func ClientRequestIDInterceptorV2() connectv2.ClientInterceptor {
	return func(next connectv2.ClientFunc) connectv2.ClientFunc {
		return func(ctx context.Context, spec connectv2.Spec) (connectv2.ClientStream, error) {
			if rid := RequestIDFromContext(ctx); rid != "" {
				if info, ok := connectv2.CallInfoForClientContext(ctx); ok && info.RequestHeader().Get(RequestIDHeader) == "" {
					info.RequestHeader().Set(RequestIDHeader, rid)
				}
			}
			return next(ctx, spec)
		}
	}
}
