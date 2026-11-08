// File: transport.go — request-ID forwarding for PLAIN outbound HTTP.
//
// ClientRequestIDInterceptor (client.go) forwards the correlation ID for
// outbound CONNECT calls. A service that talks to a third-party REST API does
// so through a plain *http.Client, which never sees a Connect interceptor — so
// without this the hop leaves the process with no X-Request-Id and the two
// sides' logs cannot be stitched together.
//
// This is a RoundTripper rather than a whole client so it composes with the
// OTel instrumentation instead of competing with it. otelhttp goes OUTSIDE, so
// its client span covers the full round trip:
//
//	transport := otelhttp.NewTransport(observe.NewRequestIDTransport(http.DefaultTransport))
//	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
//
// Note this is the opposite of ClientStackDeps.Transport, which must NOT be
// wrapped in otelhttp: there the otelconnect interceptor already opens the
// client span, and wrapping would nest a duplicate span under every RPC.
package observe

import "net/http"

// NewRequestIDTransport returns an http.RoundTripper that copies the
// correlation ID already on the request context (RequestIDFromContext — put
// there by the inbound RequestIDInterceptor or the HTTP RequestIDMiddleware)
// onto the outgoing RequestIDHeader, so the callee's logs carry the same
// request_id as the caller's.
//
// An ID already present on the outgoing header is left untouched, and a
// context without an ID forwards nothing (the callee mints its own). A nil
// base uses http.DefaultTransport.
//
// Wrap it in otelhttp for traces — request-ID forwarding and OTel spans are
// independent concerns and you want both; see the file header for the
// canonical composition.
func NewRequestIDTransport(base http.RoundTripper) http.RoundTripper {
	return &requestIDTransport{base: base}
}

type requestIDTransport struct {
	base http.RoundTripper
}

func (t *requestIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	rid := RequestIDFromContext(req.Context())
	if rid == "" || req.Header.Get(RequestIDHeader) != "" {
		return base.RoundTrip(req)
	}

	// RoundTrippers must not mutate the request they are handed — the caller
	// may retry or inspect it. Clone shares the body (and GetBody) but gives
	// us our own header map to write into.
	clone := req.Clone(req.Context())
	clone.Header.Set(RequestIDHeader, rid)
	return base.RoundTrip(clone)
}
