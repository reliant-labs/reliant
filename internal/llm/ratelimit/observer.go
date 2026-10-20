// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"context"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Observer receives the Snapshot of every provider response that carried
// rate-limit data, success and error alike. Implementations must be fast and
// safe for concurrent use: they run on the request path.
//
// The Observer declared here is the one the driver-side hook consumes; the
// installer (see WithObserver) is the party that knows which credential the
// request used and attaches that identity to what it persists.
type Observer interface {
	ObserveRateLimits(ctx context.Context, s Snapshot)
}

type observerKey struct{}

// WithObserver returns a context whose LLM requests report to o.
func WithObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, o)
}

// Parser turns response headers into a Snapshot; ok is false when the response
// carried no rate-limit data for its format.
type Parser func(h http.Header, now time.Time) (Snapshot, bool)

// Observe stamps s with provider and reports it to the context's Observer, or
// to the default (a debug log) when none is installed.
func Observe(ctx context.Context, provider string, s Snapshot) {
	s.Provider = provider
	if o, ok := ctx.Value(observerKey{}).(Observer); ok && o != nil {
		o.ObserveRateLimits(ctx, s)
		return
	}
	logging.DebugContext(ctx, "LLM rate-limit snapshot", "provider", provider, "status", string(s.Status),
		"windows", len(s.Windows), "representative", s.RepresentativeWindow)
}

// ObserveHeaders parses h with parse and reports the result, if any.
func ObserveHeaders(ctx context.Context, provider string, parse Parser, h http.Header) {
	if len(h) == 0 {
		return
	}
	if s, ok := parse(h, time.Now()); ok {
		Observe(ctx, provider, s)
	}
}

// Middleware returns an SDK request middleware (assignable to both
// anthropic-sdk-go's and openai-go's option.Middleware) that reports every
// response's headers — including error responses — through ObserveHeaders. The
// request's context carries the Observer.
func Middleware(provider string, parse Parser) func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		res, err := next(req)
		if res != nil {
			ObserveHeaders(req.Context(), provider, parse, res.Header)
		}
		return res, err
	}
}
