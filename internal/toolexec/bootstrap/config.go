package bootstrap

import (
	"fmt"
	"net/url"
	"strings"
)

// TLSMode controls daemon transport security/protocol behavior.
type TLSMode string

const (
	TLSModeTLS                   TLSMode = "tls"
	TLSModeInsecureTLSSkipVerify TLSMode = "insecure_tls_skip_verify"
	TLSModeH2C                   TLSMode = "h2c"
	TLSModeDisabled              TLSMode = "disabled" // alias for h2c
)

// DaemonBootstrapConfig is the explicit launcher-provided config for tools-daemon.
// user_id is no longer carried here — the gateway derives it from the PAT and
// returns it in the RegistrationAck.
type DaemonBootstrapConfig struct {
	AuthToken string
	GRPCURL   string
	TLSMode   TLSMode
	DataDir   string
	Name      string // Human-friendly daemon name (default: hostname)

	// ServerURL is the API server origin (scheme://host:port) these
	// credentials belong to. Used as the per-origin key into
	// ~/.reliant/daemon.json so the daemon can persist the server-assigned
	// DaemonID after registration. Empty in server mode (the gateway dials
	// in and already knows our identity).
	ServerURL string

	// DaemonID is the stable identity previously assigned by the server for
	// ServerURL's origin, read from persisted credentials at startup. The
	// daemon re-asserts it in its registration message so identity survives
	// restarts and hostname changes. Empty on first-ever registration.
	DaemonID string

	// ServerMode, when true, makes the daemon listen on ListenPort for
	// incoming gateway connections instead of dialing out.
	ServerMode bool
	ListenPort int // default 9190
}

// NormalizeGatewayURL maps the gateway address forms an operator can plausibly
// be handed onto the two schemes the daemon's HTTP/2 transport can actually
// dial.
//
// `forge cluster urls` prints the daemon gateway as grpc://host:port, which is
// the natural thing to paste into --grpc-url. golang.org/x/net/http2 rejects
// any scheme other than http/https ("http2: unsupported scheme"), and
// connect-go surfaces that on a bidi stream as `write envelope: EOF` — so the
// daemon appeared to start and then silently served nothing. grpc:// is
// unambiguous (plaintext h2c) and grpcs:// is unambiguous (TLS), so both are
// accepted and rewritten rather than rejected. Everything else is rejected up
// front naming the schemes that work: a clear error at startup beats a process
// that runs forever without a stream.
func NormalizeGatewayURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("missing daemon gateway URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid daemon gateway URL %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "http", "https":
		return trimmed, nil
	case "grpc":
		parsed.Scheme = "http"
		return parsed.String(), nil
	case "grpcs":
		parsed.Scheme = "https"
		return parsed.String(), nil
	default:
		return "", fmt.Errorf(
			"unsupported daemon gateway URL %q: scheme %q cannot be dialed — use http:// (or grpc://) for plaintext h2c, https:// (or grpcs://) for TLS",
			raw, parsed.Scheme)
	}
}

// GatewayURL returns the dialable gateway URL for this config.
func (c DaemonBootstrapConfig) GatewayURL() (string, error) {
	return NormalizeGatewayURL(c.GRPCURL)
}

func (c DaemonBootstrapConfig) Validate() error {
	if !c.ServerMode && strings.TrimSpace(c.AuthToken) == "" {
		return fmt.Errorf("missing required daemon PAT (run 'reliant daemon register' to set up credentials)")
	}
	if !c.ServerMode {
		if _, err := NormalizeGatewayURL(c.GRPCURL); err != nil {
			return err
		}
	}
	switch c.TLSMode {
	case TLSModeTLS, TLSModeInsecureTLSSkipVerify, TLSModeH2C, TLSModeDisabled:
		return nil
	case "":
		if c.ServerMode {
			return nil // TLS mode is optional in server mode
		}
		return fmt.Errorf("invalid RELIANT_DAEMON_TLS_MODE %q", c.TLSMode)
	default:
		return fmt.Errorf("invalid RELIANT_DAEMON_TLS_MODE %q", c.TLSMode)
	}
}
