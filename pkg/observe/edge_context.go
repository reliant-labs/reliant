package observe

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"connectrpc.com/connect"
)

// The edge visitor context. On Reliant hosting every request reaches a
// workload through the platform's proxy, which stamps two headers and, once
// control-plane #2517 is deployed, strips any copy a client sent:
//
//   - X-Reliant-Visitor-Day: the visitor's cookieless daily hash, the same one
//     it stamps on that browser's own telemetry;
//   - X-Reliant-Privacy: "gpc" or "dnt" when the browser asked not to be
//     tracked (and then no visitor hash at all).
//
// EdgeContextMiddleware (HTTP) and EdgeContextInterceptor (Connect) read them
// into the request's context, and Track and TrackAuth record the hash as
// visitor.day: that is what links a server-side sign-up to the anonymous visit
// that led to it, with no browser code. A client can send these headers
// wherever no proxy strips them, so they are read only when the process
// declares the edge trusted (EnvTrustedEdge=reliant); otherwise they are
// ignored. forge does not render that variable for any workload yet, so they
// are ignored everywhere unless an author sets it by hand; a later forge
// renders it for forge.OnHosted workloads once the stripping proxy is
// deployed.

// Edge headers and the attribute the visitor hash is recorded as.
const (
	HeaderVisitorDay = "X-Reliant-Visitor-Day"
	HeaderPrivacy    = "X-Reliant-Privacy"
	// AttrVisitorDay is the product-event attribute carrying the hash.
	AttrVisitorDay = "visitor.day"
	// EnvTrustedEdge names the edge that fronts this process. "reliant" (Reliant
	// hosting) makes the edge headers trusted; anything else, or unset, ignores
	// them.
	EnvTrustedEdge = "FORGE_TRUSTED_EDGE"
	// TrustedEdgeReliant is EnvTrustedEdge's value on Reliant hosting.
	TrustedEdgeReliant = "reliant"
)

// visitorDayRE is the hash's shape: 64 bits, lowercase hex. Anything else is
// not the proxy's and is dropped.
var visitorDayRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

var edgeTrusted = sync.OnceValue(func() bool { return envValue(EnvTrustedEdge) == TrustedEdgeReliant })

type edgeContextKey struct{}

// EdgeContext is what the trusted edge said about a request's browser.
type EdgeContext struct {
	// VisitorDay is the visitor's daily hash; "" when the edge gave none (a
	// bot, an unresolvable address, a privacy signal, no trusted edge).
	VisitorDay string
	// Privacy is "gpc" or "dnt" when the browser asked not to be tracked.
	Privacy string
}

// WithEdgeContext returns ctx carrying e. A privacy signal drops the visitor
// hash, whatever e says: such a browser is never linked.
func WithEdgeContext(ctx context.Context, e EdgeContext) context.Context {
	if e.Privacy != "" || !visitorDayRE.MatchString(e.VisitorDay) {
		e.VisitorDay = ""
	}
	return context.WithValue(ctx, edgeContextKey{}, e)
}

// EdgeContextFrom returns the edge context of ctx, zero when there is none.
func EdgeContextFrom(ctx context.Context) EdgeContext {
	if ctx == nil {
		return EdgeContext{}
	}
	e, _ := ctx.Value(edgeContextKey{}).(EdgeContext)
	return e
}

// edgeContextFromHeader reads h, when the edge is trusted.
func edgeContextFromHeader(ctx context.Context, h http.Header, trusted bool) context.Context {
	if !trusted {
		return ctx
	}
	vd, pr := strings.TrimSpace(h.Get(HeaderVisitorDay)), strings.ToLower(strings.TrimSpace(h.Get(HeaderPrivacy)))
	if vd == "" && pr == "" {
		return ctx
	}
	switch pr {
	case "", "gpc", "dnt":
	default:
		pr = "other"
	}
	return WithEdgeContext(ctx, EdgeContext{VisitorDay: vd, Privacy: pr})
}

// EdgeContextMiddleware reads the trusted edge's headers into every request's
// context (see the file comment). With no trusted edge it passes the request
// through untouched. serverkit installs it on every listener.
func EdgeContextMiddleware(next http.Handler) http.Handler {
	return edgeContextMiddleware(next, edgeTrusted())
}

func edgeContextMiddleware(next http.Handler, trusted bool) http.Handler {
	if !trusted {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx := edgeContextFromHeader(r.Context(), r.Header, true); ctx != r.Context() {
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// EdgeContextInterceptor is EdgeContextMiddleware for a Connect handler
// served behind some other HTTP stack. Chain installs it.
func EdgeContextInterceptor() connect.Interceptor {
	return edgeContextInterceptor{trusted: edgeTrusted()}
}

type edgeContextInterceptor struct{ trusted bool }

func (e edgeContextInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	if !e.trusted {
		return next
	}
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			ctx = edgeContextFromHeader(ctx, req.Header(), true)
		}
		return next(ctx, req)
	}
}

func (edgeContextInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (e edgeContextInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	if !e.trusted {
		return next
	}
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(edgeContextFromHeader(ctx, conn.RequestHeader(), true), conn)
	}
}
