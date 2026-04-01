package transport

import (
	"crypto/tls"
	"fmt"
	"net/http"

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
		return wrap(&http.Transport{}), cfg.GRPCURL, nil
	case bootstrap.TLSModeTLS:
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: false, MinVersion: tls.VersionTLS12},
		}
		return wrap(tr), cfg.GRPCURL, nil
	case bootstrap.TLSModeInsecureTLSSkipVerify:
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
		return wrap(tr), cfg.GRPCURL, nil
	default:
		return nil, "", fmt.Errorf("unsupported TLS mode: %q", cfg.TLSMode)
	}
}
