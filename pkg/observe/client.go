// File: client.go — the OUTBOUND Connect client stack.
//
// This is the client-side counterpart to Chain/DefaultMiddlewares (the
// server-side interceptor chain). When a service that used to be bound
// in-process is split out into its own deployment, the consumer's wiring in
// internal/app/providers.go changes from "construct the package and hand the
// interface down" to "construct a generated Connect client over HTTP". THIS
// is the canonical way to build that client's HTTP stack:
//
//	stack, err := observe.NewClientStack(observe.ClientStackDeps{})
//	if err != nil { ... }
//	infra.Users = usersv1connect.NewUsersServiceClient(
//	    stack.HTTPClient, cfg.UsersBaseURL, stack.ClientOptions...,
//	)
//
// The stack bundles the three things every outbound Connect client needs and
// that are easy to forget individually:
//
//   - the otelconnect CLIENT interceptor — one OTel client span per RPC,
//     rpc.client.* metrics, and W3C trace-context propagation onto the wire
//     (the global propagator installed by observe.Setup), so the callee's
//     server span joins the caller's trace;
//   - request-ID forwarding — the X-Request-Id already on ctx (put there by
//     the server-side RequestIDInterceptor / HTTP middleware) is copied onto
//     the outbound request, so logs correlate across the hop;
//   - a default timeout on the http.Client, so a hung downstream can never
//     wedge the caller indefinitely.
package observe

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
)

// defaultClientTimeout bounds outbound requests when ClientStackDeps.Timeout
// is left zero. 30s matches the scaffolded Infra.DefaultClient() default.
const defaultClientTimeout = 30 * time.Second

// ClientStackDeps configures NewClientStack. The zero value is a working
// production default: shared http.DefaultTransport pool, 30s timeout,
// otelconnect + request-ID forwarding interceptors.
type ClientStackDeps struct {
	// Timeout bounds each outbound request end-to-end (http.Client.Timeout).
	// Zero applies the 30s default. Negative disables the timeout entirely —
	// required for long-lived server-streaming RPCs, which the default
	// timeout would sever mid-stream.
	Timeout time.Duration

	// Transport is the base RoundTripper under the client. nil uses
	// http.DefaultTransport — the process-wide shared connection pool, which
	// is what you want unless the target needs special TLS/proxy settings.
	// Do NOT wrap this in otelhttp: the otelconnect interceptor already
	// creates the client span and injects propagation headers; doubling up
	// yields nested duplicate client spans per RPC.
	Transport http.RoundTripper

	// Extras are appended after the canonical client interceptors
	// (otelconnect, request-ID forwarding), in the order supplied — the slot
	// for project-specific concerns like auth-token injection.
	Extras []connect.Interceptor
}

// ClientStack is what a generated Connect client constructor consumes:
//
//	foov1connect.NewFooServiceClient(stack.HTTPClient, baseURL, stack.ClientOptions...)
type ClientStack struct {
	// HTTPClient carries the timeout and the base transport. It is freshly
	// constructed per stack — callers may tweak it (CheckRedirect, Jar)
	// without affecting other stacks.
	HTTPClient *http.Client

	// ClientOptions carries the interceptor chain as connect.ClientOption
	// values, ready to splat into the generated New<Svc>Client call.
	ClientOptions []connect.ClientOption
}

// NewClientStack builds the canonical outbound Connect client stack. See the
// file header for what it bundles and the providers.go usage shape. The
// error surface is otelconnect interceptor construction (never fails under a
// correctly-installed OTel SDK, but is not swallowed — an uninstrumented
// outbound edge should be loud).
func NewClientStack(deps ClientStackDeps) (ClientStack, error) {
	timeout := deps.Timeout
	switch {
	case timeout == 0:
		timeout = defaultClientTimeout
	case timeout < 0:
		timeout = 0
	}

	otelInt, err := otelconnect.NewInterceptor()
	if err != nil {
		return ClientStack{}, fmt.Errorf("otelconnect client interceptor: %w", err)
	}

	chain := make([]connect.Interceptor, 0, 2+len(deps.Extras))
	chain = append(chain, otelInt, ClientRequestIDInterceptor())
	chain = append(chain, deps.Extras...)

	return ClientStack{
		HTTPClient: &http.Client{
			Transport: deps.Transport,
			Timeout:   timeout,
		},
		ClientOptions: []connect.ClientOption{connect.WithInterceptors(chain...)},
	}, nil
}

// ClientRequestIDInterceptor returns a Connect interceptor for OUTBOUND
// clients that forwards the correlation ID already on ctx (see
// RequestIDFromContext) as the RequestIDHeader on the outgoing request, so
// the callee's logs carry the same request_id as the caller's. An ID already
// present on the outgoing header is left untouched; a ctx without an ID
// forwards nothing (the callee mints its own).
//
// It is included in NewClientStack's canonical chain; it is exported for
// projects assembling a bespoke client chain.
func ClientRequestIDInterceptor() connect.Interceptor {
	return &clientRequestIDInterceptor{}
}

type clientRequestIDInterceptor struct{}

func (i *clientRequestIDInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if rid := RequestIDFromContext(ctx); rid != "" && req.Header().Get(RequestIDHeader) == "" {
			req.Header().Set(RequestIDHeader, rid)
		}
		return next(ctx, req)
	})
}

func (i *clientRequestIDInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return connect.StreamingClientFunc(func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if rid := RequestIDFromContext(ctx); rid != "" && conn.RequestHeader().Get(RequestIDHeader) == "" {
			conn.RequestHeader().Set(RequestIDHeader, rid)
		}
		return conn
	})
}

func (i *clientRequestIDInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
