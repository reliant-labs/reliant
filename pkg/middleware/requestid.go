package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// RequestIDHeader is the canonical header carrying the per-request
// correlation ID on both the inbound request and the outbound response.
// It mirrors observe.RequestIDHeader so the HTTP layer (this middleware)
// and the Connect layer (observe's interceptors) stay in agreement.
const RequestIDHeader = observe.RequestIDHeader

// RequestIDFromContext returns the request ID attached to ctx, or "" if
// none is set. Useful for handlers and downstream logging sites that
// want to include the ID on custom log lines. Nil ctx returns ""
// (never panics) so callers in CLI/worker contexts can share the same
// helper.
//
// The storage is pkg/observe's context key: a request ID set by this
// HTTP middleware is visible to observe.LoggingInterceptor (and vice
// versa) without a header round-trip.
func RequestIDFromContext(ctx context.Context) string {
	return observe.RequestIDFromContext(ctx)
}

// ContextWithRequestID returns a copy of ctx with the given request ID
// attached. Exposed so tests and non-HTTP entrypoints can propagate the
// value into code paths that read RequestIDFromContext.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return observe.ContextWithRequestID(ctx, id)
}

// RequestIDMiddleware is an HTTP middleware that ensures every request
// carries a correlation ID. It must be wired ahead of the logging
// middleware so log records inherit the generated ID.
//
// Behavior:
//
//   - If the inbound request has a non-empty RequestIDHeader, that value
//     is trusted and reused. This lets edge proxies and upstream services
//     stitch together a single trace across hops without coordination.
//
//   - Otherwise a 16-byte crypto/rand hex token is minted — the same
//     shape observe.RequestIDInterceptor mints at the Connect layer, so
//     IDs look uniform whichever layer assigned them. crypto/rand keeps
//     IDs unpredictable to third parties that might otherwise enumerate
//     them.
//
//   - The chosen ID is exposed to downstream middleware/handlers via the
//     request context (RequestIDFromContext), stamped back onto the
//     INBOUND request header, and echoed on the response header so the
//     client can log it for later correlation.
//
// This middleware OWNS the response echo for every request that passes
// through it. observe.RequestIDInterceptor, which runs inside it on the
// Connect path, adopts the context value and deliberately writes no
// response header of its own — otherwise connect merges its header into
// the one already written here and the response carries two conflicting
// X-Request-Id values.
//
// The inbound stamp exists for the same reason: handlers and interceptors
// that reach for the request header rather than the context must see the
// SAME id, not an empty string that invites them to mint another.
func RequestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" {
				id = newRequestID()
				r.Header.Set(RequestIDHeader, id)
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
		})
	}
}

// newRequestID mints a 16-byte random hex token. A crypto/rand failure
// is exceptional; fall back to a monotonically-distinguishable token
// rather than panicking inside the serving path.
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("rid-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
