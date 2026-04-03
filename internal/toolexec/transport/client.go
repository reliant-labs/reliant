package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"

	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

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
	case bootstrap.TLSModeH2C:
		tr := &http2.Transport{
			AllowHTTP:       true,
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
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
