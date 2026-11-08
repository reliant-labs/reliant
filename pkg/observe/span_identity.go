// File: span_identity.go — whom a server span served, and from which browser session.
//
// Two attributes join a backend span to the person and the page behind it:
//
//   - user.id (AttrUserID): the AUTHENTICATED caller's user id, read from the
//     claims the auth interceptor stashed, never from anything the request says.
//     It is set after Auth, on the server span and on the innermost span.
//   - session.id (AttrSessionID): the browser's RUM session id, read from the W3C
//     baggage member of that name the forge web runtime sends with every Connect
//     call. It is a CLIENT CLAIM and is used only to correlate: the console links a
//     trace to the session replay of the same org, and nothing reads it to decide
//     access. So it is accepted from every caller, at every trace-trust level, but
//     only when it has the shape of a session id, and it is set on the server span
//     only. The baggage itself is never put into the request's context, so it does
//     not ride along to downstream services (trust_remote.go, threat 3).
//
// Neither attribute is a metric label: MetricsInterceptor's attribute set is
// fixed (rpc.*), so a user or session id never becomes a time series.

package observe

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/authn"
)

// AttrSessionID is the browser RUM session id on a backend span, and the baggage
// member the web runtime propagates it in.
const AttrSessionID = "session.id"

// CallerLookup returns the authenticated caller of a request, after Auth ran.
type CallerLookup = func(ctx context.Context) (*authn.Claims, bool)

// sessionIDRe is the shape of a RUM session id (32 hex digits from the browser
// SDK); wider, as obsquery.CheckSessionID is, so a custom id survives.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type serverSpanKey struct{}

// serverSpanOf is the span RequestSpanInterceptor recorded as the request's
// server span, or the context's span when none was recorded.
func serverSpanOf(ctx context.Context) trace.Span {
	if s, ok := ctx.Value(serverSpanKey{}).(trace.Span); ok {
		return s
	}
	return trace.SpanFromContext(ctx)
}

// SessionIDFromBaggage returns the first session.id member of the request's W3C
// baggage that has the shape of a session id, else "". Each list member is
// parsed on its own, so a malformed member another party added (or one that
// pushes the whole header past baggage's size limits) does not hide the session.
func SessionIDFromBaggage(header http.Header) string {
	for _, value := range header.Values("baggage") {
		for _, member := range strings.Split(value, ",") {
			b, err := baggage.Parse(member)
			if err != nil {
				continue
			}
			if id := b.Member(AttrSessionID).Value(); sessionIDRe.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

// RequestSpanInterceptor records the request's server span (the one the Trace
// slot opened) for identitySpanInterceptor, and sets session.id on it from the
// request's baggage. Chain installs it directly inside Trace.
func RequestSpanInterceptor() connect.Interceptor { return requestSpan{} }

type requestSpan struct{}

func (requestSpan) enter(ctx context.Context, header http.Header) context.Context {
	span := trace.SpanFromContext(ctx)
	if id := SessionIDFromBaggage(header); id != "" {
		span.SetAttributes(attribute.String(AttrSessionID, id))
	}
	return context.WithValue(ctx, serverSpanKey{}, span)
}

func (r requestSpan) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		return next(r.enter(ctx, req.Header()), req)
	}
}

func (requestSpan) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (r requestSpan) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(r.enter(ctx, conn.RequestHeader()), conn)
	}
}

// IdentitySpanInterceptor sets user.id on the request's server span (and on the
// innermost span, when that is another) from the authenticated caller. It must
// run after Auth; Chain installs it there. A nil caller reads forge's own claims
// (authn.ClaimsFromContext).
func IdentitySpanInterceptor(caller CallerLookup) connect.Interceptor {
	if caller == nil {
		caller = authn.ClaimsFromContext
	}
	return identitySpan{caller: caller}
}

type identitySpan struct{ caller CallerLookup }

func (i identitySpan) stamp(ctx context.Context) {
	claims, ok := i.caller(ctx)
	if !ok || claims == nil || claims.UserID == "" {
		return
	}
	attr := attribute.String(AttrUserID, claims.UserID)
	server := serverSpanOf(ctx)
	server.SetAttributes(attr)
	if inner := trace.SpanFromContext(ctx); inner.SpanContext().SpanID() != server.SpanContext().SpanID() {
		inner.SetAttributes(attr)
	}
}

func (i identitySpan) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			i.stamp(ctx)
		}
		return next(ctx, req)
	}
}

func (identitySpan) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i identitySpan) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		i.stamp(ctx)
		return next(ctx, conn)
	}
}
