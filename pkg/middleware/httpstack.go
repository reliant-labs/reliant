package middleware

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/observe"
)

// HTTPStack returns HTTP middleware that applies recovery, logging, and
// audit to plain HTTP handlers. This provides the same cross-cutting
// concerns as the Connect interceptors for routes that cannot use the
// Connect protocol (e.g. webhooks, OAuth callbacks, REST endpoints).
//
// claimsFrom is the project's ClaimsFromContext (see [ClaimsLookup]);
// nil produces anonymous audit entries.
//
// Auth is NOT included because REST routes often have different auth
// requirements (e.g. webhook signature verification instead of JWT).
// Use [HTTPAuth] separately for routes that need Bearer authentication.
func HTTPStack(logger *slog.Logger, claimsFrom ClaimsLookup) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		// Apply in reverse order: recovery is outermost.
		h := next
		h = httpAudit(logger, claimsFrom)(h)
		h = httpLogging(logger)(h)
		h = httpRecovery(logger)(h)
		return h
	}
}

// HTTPAuth returns HTTP middleware that validates Bearer tokens and
// populates the context with claims, mirroring the Connect auth
// interceptor for plain HTTP routes.
//
// authenticate validates the raw token and returns the claims — pass
// the project's ValidateToken. withClaims stashes them on the context —
// pass the project's ContextWithClaims (the project owns the claims
// context key). If authenticate is nil (dev mode), all requests are
// allowed through untouched.
func HTTPAuth(
	authenticate func(token string) (*auth.Claims, error),
	withClaims func(ctx context.Context, claims *auth.Claims) context.Context,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticate == nil {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(authHeader, "Bearer ")

			claims, err := authenticate(token)
			if err != nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			ctx := r.Context()
			if withClaims != nil {
				ctx = withClaims(ctx, claims)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// httpRecovery returns HTTP middleware that recovers from panics, logs the
// panic and stack trace, and returns a 500 Internal Server Error.
func httpRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					observe.LogPanic(r.Context(), logger, "panic recovered", rec,
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// httpLogging returns HTTP middleware that logs the method, path, duration,
// and status code for every request.
func httpLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := newStatusRecorder(w)

			next.ServeHTTP(rec, r)

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
			}
			if rid := RequestIDFromContext(r.Context()); rid != "" {
				attrs = append(attrs, slog.String("request_id", rid))
			}

			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "http request", attrs...)
		})
	}
}

// httpAudit returns HTTP middleware that produces audit log entries for HTTP
// requests. Audit logs capture: who (from claims), what (method + path),
// when, result (status code), and duration.
func httpAudit(logger *slog.Logger, claimsFrom ClaimsLookup) func(http.Handler) http.Handler {
	audit := logger.With("log_type", "audit")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := newStatusRecorder(w)

			next.ServeHTTP(rec, r)

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("peer", r.RemoteAddr),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
				slog.Time("timestamp", start),
			}
			// Same join key as the Connect audit record: the id the client
			// was handed. Without it the audit trail cannot be reached from
			// the only identifier an incident actually starts with.
			if rid := RequestIDFromContext(r.Context()); rid != "" {
				attrs = append(attrs, slog.String("request_id", rid))
			}

			// user_id only: this line is emitted per request, and an email
			// in it would be personal data in every log line.
			var identified bool
			if claimsFrom != nil {
				if claims, ok := claimsFrom(r.Context()); ok && claims != nil {
					attrs = append(attrs, slog.String("user_id", claims.UserID))
					identified = true
				}
			}
			if !identified {
				attrs = append(attrs, slog.String("user_id", "anonymous"))
			}

			if rec.status >= 400 {
				attrs = append(attrs, slog.String("status_text", http.StatusText(rec.status)))
				audit.LogAttrs(r.Context(), slog.LevelWarn, "audit", attrs...)
			} else {
				audit.LogAttrs(r.Context(), slog.LevelInfo, "audit", attrs...)
			}
		})
	}
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying ResponseWriter, enabling middleware like
// http.ResponseController to access it.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Flush implements http.Flusher if the underlying ResponseWriter supports it.
// This is required for SSE (server-sent events) to work correctly.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker if the underlying ResponseWriter supports it.
// This is required for WebSocket upgrades to work correctly.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// compile-time checks
var (
	_ http.Flusher  = (*statusRecorder)(nil)
	_ http.Hijacker = (*statusRecorder)(nil)
)
