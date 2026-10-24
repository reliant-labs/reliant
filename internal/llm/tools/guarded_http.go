package tools

import (
	"context"
	"net"
	"net/http"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"

	"github.com/reliant-labs/reliant/internal/netguard"
)

// webToolTransport builds the dial-guarded transport for every outbound HTTP
// client of an in-process (PlacementServer/PlacementAny) tool, under the
// deployment's policy (ToolsOptions.AllowPrivateEgress). Tests override it.
var webToolTransport = func(policy netguard.Policy) http.RoundTripper {
	return policy.Transport()
}

// newGuardedHTTPClient is the only way tools in this package may build an
// http.Client: hosted deployments cannot dial private, loopback, link-local or
// metadata addresses, including via redirects or DNS rebinding.
func newGuardedHTTPClient(policy netguard.Policy, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: tracedEgress(webToolTransport(policy))}
}

// newFetchNetworkClient is newGuardedHTTPClient for fetch running on a daemon:
// the same guarantees (every dial, redirect hop and DNS answer is checked at
// the socket, no ambient proxy) under that daemon's FetchNetwork instead of a
// netguard.Policy. It lives here because this file is the only place in the
// package allowed to build a transport.
func newFetchNetworkClient(n FetchNetwork, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs on the address about to be connected to, after DNS, so
		// a name that resolves (or rebinds) to a forbidden address is refused
		// at the socket.
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			return n.CheckIP(net.ParseIP(host))
		},
	}
	return &http.Client{Timeout: timeout, Transport: tracedEgress(&http.Transport{
		Proxy: nil, // a proxy would be dialed instead of the destination, defeating the checks
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if err := n.checkHost(host); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	})}
}

// tracedEgress records a client span for each request a tool makes, so the
// call shows up in the run's trace. It injects no trace headers: the
// destination is a URL a user or model chose, and our trace ids are not
// theirs to see.
func tracedEgress(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()))
}
