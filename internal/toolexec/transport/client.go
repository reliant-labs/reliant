// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: client.go builds the dialer the daemon transport uses; it is wiring for the gRPC transport, not a service boundary; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: daemon transport client construction (TLS / h2c dialing); SDK wiring
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
	"github.com/reliant-labs/reliant/internal/version"
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

// ReliantClientHeader names the first-party client and its version on every
// request to a Reliant server: `<kind>/<semver without a leading v>`, kind one
// of web, desktop, daemon, cli, sdk-go, sdk-ts. A server may refuse a client
// below its floor, or one that sends no header at all, with
// errreason.UpgradeRequired, so every reliant.v1 call carries it.
const ReliantClientHeader = "Reliant-Client"

// DaemonClientKind is the daemon's kind in ReliantClientHeader.
const DaemonClientKind = "daemon"

// ClientIdentity is this binary's ReliantClientHeader value for kind. A build
// with no version stamp sends "<kind>/unknown", which a server never forces.
func ClientIdentity(kind string) string {
	return kind + "/" + strings.TrimPrefix(version.Get().Version, "v")
}

type daemonAuthRoundTripper struct {
	next      http.RoundTripper
	authToken string
	// client is the ReliantClientHeader value, set on every request: the
	// gateway stream's open and every unary call alike.
	client string
}

func (t daemonAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if req.Header.Get(daemonAuthorizationHeader) == "" {
		req.Header.Set(daemonAuthorizationHeader, "Bearer "+t.authToken)
	}
	if t.client != "" {
		req.Header.Set(ReliantClientHeader, t.client)
	}
	return t.next.RoundTrip(req)
}

func NewDaemonHTTPClient(cfg bootstrap.DaemonBootstrapConfig) (*http.Client, string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, "", err
	}

	// The transport is the single choke point where a gateway URL becomes a
	// dial, so normalization lands here: every caller gets a URL http2 can
	// actually dial, or a named error, never a silent non-connection.
	baseURL := cfg.GRPCURL
	if !cfg.ServerMode {
		normalized, err := cfg.GatewayURL()
		if err != nil {
			return nil, "", err
		}
		baseURL = normalized
	}

	wrap := func(rt http.RoundTripper) *http.Client {
		return &http.Client{Transport: daemonAuthRoundTripper{next: rt, authToken: cfg.AuthToken, client: ClientIdentity(DaemonClientKind)}, Timeout: 0}
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
		return wrap(tr), baseURL, nil
	case bootstrap.TLSModeTLS:
		tr := &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false, MinVersion: tls.VersionTLS12},
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
		}
		return wrap(tr), baseURL, nil
	case bootstrap.TLSModeInsecureTLSSkipVerify:
		tr := &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
		}
		return wrap(tr), baseURL, nil
	default:
		return nil, "", fmt.Errorf("unsupported TLS mode: %q", cfg.TLSMode)
	}
}
