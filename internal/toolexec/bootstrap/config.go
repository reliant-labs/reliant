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

	// ServerMode, when true, makes the daemon listen on ListenPort for
	// incoming gateway connections instead of dialing out.
	ServerMode bool
	ListenPort int // default 9190
}

func (c DaemonBootstrapConfig) Validate() error {
	if !c.ServerMode && strings.TrimSpace(c.AuthToken) == "" {
		return fmt.Errorf("missing required daemon PAT (run 'reliant daemon register' to set up credentials)")
	}
	if !c.ServerMode {
		if strings.TrimSpace(c.GRPCURL) == "" {
			return fmt.Errorf("missing required RELIANT_DAEMON_GRPC_URL")
		}
		if _, err := url.Parse(c.GRPCURL); err != nil {
			return fmt.Errorf("invalid RELIANT_DAEMON_GRPC_URL: %w", err)
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
