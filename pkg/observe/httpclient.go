// File: httpclient.go — the traced *http.Client for PLAIN outbound HTTP.
//
// A call to a third-party REST API (Stripe, GitHub, GCS, a webhook) goes
// through a plain *http.Client, which no Connect interceptor sees. Built by
// hand, that client is untraced: no client span, no traceparent on the
// request, no X-Request-Id, and the trace of the request that made the call
// ends at the process boundary. NewHTTPClient is the one constructor that
// gets all three right, so projects stop re-composing otelhttp and the
// request-ID transport (and stop forgetting to).
//
// For CONNECT clients use NewClientStack instead: there the otelconnect
// interceptor opens the client span, and an otelhttp transport underneath
// would nest a duplicate span under every RPC.

package observe

import (
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPClientOptions configures NewHTTPClient. The zero value is the
// production default.
type HTTPClientOptions struct {
	// Timeout bounds each request end to end (http.Client.Timeout). Zero
	// applies the 30s default; negative disables it (a long-poll or a
	// streaming download).
	Timeout time.Duration

	// Base is the RoundTripper under the instrumentation. nil shares one
	// process-wide traced transport over http.DefaultTransport — one
	// connection pool. Set it only for a target that needs its own TLS or
	// proxy settings; it is wrapped, never replaced, so it is traced too.
	Base http.RoundTripper
}

// NewHTTPClient returns a fresh *http.Client whose every request is an OTel
// CLIENT span ("<METHOD> <host>"), carries the caller's trace context (W3C
// traceparent, through the global propagator observe.Setup installs) and its
// X-Request-Id. Fresh per call: a consumer may change CheckRedirect or Jar on
// it without affecting anyone else, while the default transport, and so the
// connection pool, is shared.
func NewHTTPClient(opts HTTPClientOptions) *http.Client {
	timeout := opts.Timeout
	switch {
	case timeout == 0:
		timeout = defaultClientTimeout
	case timeout < 0:
		timeout = 0
	}
	transport := sharedHTTPTransport()
	if opts.Base != nil {
		transport = NewHTTPTransport(opts.Base)
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// NewHTTPTransport wraps base (nil: http.DefaultTransport) so every request
// through it is traced and forwards the request ID. otelhttp sits OUTSIDE the
// request-ID transport, so its span covers the whole round trip including the
// header the inner layer adds. Prefer NewHTTPClient; this is for a client
// whose construction you do not own.
func NewHTTPTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(NewRequestIDTransport(base),
		otelhttp.WithSpanNameFormatter(httpClientSpanName))
}

// httpClientSpanName names a client span "<METHOD> <host>" — "POST
// api.stripe.com". otelhttp's default is "HTTP POST" for every request, which
// makes a trace with three outbound calls read as the same call three times.
// The host is bounded (the services a process talks to); the path is not, so
// it stays an attribute.
func httpClientSpanName(_ string, r *http.Request) string {
	if r.URL == nil || r.URL.Host == "" {
		return r.Method
	}
	return r.Method + " " + r.URL.Host
}

var (
	sharedTransport     http.RoundTripper
	sharedTransportOnce sync.Once
)

// sharedHTTPTransport is the traced transport every NewHTTPClient without a
// Base shares. Built lazily: otelhttp reads the global tracer provider at
// request time, so one built before observe.Setup still reports.
func sharedHTTPTransport() http.RoundTripper {
	sharedTransportOnce.Do(func() { sharedTransport = NewHTTPTransport(nil) })
	return sharedTransport
}
