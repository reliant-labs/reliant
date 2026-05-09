package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// LocalhostDialContext returns a DialContext function that resolves *.localhost
// hostnames to 127.0.0.1. macOS does not resolve subdomain.localhost via DNS,
// so this is needed for dev multi-worktree setups where services are addressed
// as e.g. nw-wf.localhost:19090.
func LocalhostDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err == nil && strings.HasSuffix(host, ".localhost") {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

const daemonAuthorizationHeader = "Authorization"

type daemonAuthRoundTripper struct {
	next      http.RoundTripper
	authToken string
}

func (t daemonAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get(daemonAuthorizationHeader) == "" {
		req = req.Clone(req.Context())
		req.Header.Set(daemonAuthorizationHeader, "Bearer "+t.authToken)
	}
	return t.next.RoundTrip(req)
}

func NewDaemonHTTPClient(cfg bootstrap.DaemonBootstrapConfig) (*http.Client, string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, "", err
	}

	wrap := func(rt http.RoundTripper) *http.Client {
		return &http.Client{Transport: daemonAuthRoundTripper{next: rt, authToken: cfg.AuthToken}, Timeout: 0}
	}

	switch cfg.TLSMode {
	case bootstrap.TLSModeH2C, bootstrap.TLSModeDisabled:
		tr := &http2.Transport{
			AllowHTTP:       true,
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return LocalhostDialContext(ctx, network, addr)
			},
		}
		return wrap(tr), cfg.GRPCURL, nil
	case bootstrap.TLSModeTLS:
		tr := &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false, MinVersion: tls.VersionTLS12},
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
		}
		return wrap(tr), cfg.GRPCURL, nil
	case bootstrap.TLSModeInsecureTLSSkipVerify:
		tr := &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
		}
		return wrap(tr), cfg.GRPCURL, nil
	default:
		return nil, "", fmt.Errorf("unsupported TLS mode: %q", cfg.TLSMode)
	}
}
