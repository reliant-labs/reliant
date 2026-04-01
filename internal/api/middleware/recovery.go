// Copyright (c) 2025 Reliant Labs
package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// SentryRecoverer is a middleware that recovers from panics and reports them to Sentry
// It replaces chi's default Recoverer middleware with one that also reports to Sentry
func SentryRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				// Get stack trace
				stack := string(debug.Stack())

				// Log the panic
				logging.Error("[HTTP] Panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rvr,
					"stack", stack,
				)

				// Report to Sentry
				panicErr := fmt.Errorf("panic in %s %s: %v", r.Method, r.URL.Path, rvr)
				reportHTTPPanicToSentry(panicErr, r, stack)

				// Return 500 to client
				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// reportHTTPPanicToSentry reports an HTTP panic to telemetry with request context.
func reportHTTPPanicToSentry(err error, r *http.Request, stack string) {
	telemetry.CaptureExceptionWithContext(err, map[string]string{
		"type":   "http_panic",
		"method": r.Method,
		"path":   r.URL.Path,
	}, map[string]interface{}{
		"stack_trace":  stack,
		"query_string": r.URL.RawQuery,
		"user_agent":   r.UserAgent(),
		"remote_addr":  r.RemoteAddr,
	})
}
