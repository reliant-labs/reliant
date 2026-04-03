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
)

// DaemonBootstrapConfig is the explicit launcher-provided config for tools-daemon.
type DaemonBootstrapConfig struct {
	UserID    string
	AuthToken string
	GRPCURL   string
	TLSMode   TLSMode
	DataDir   string
}

func (c DaemonBootstrapConfig) Validate() error {
	if strings.TrimSpace(c.UserID) == "" {
		return fmt.Errorf("missing required RELIANT_DAEMON_USER_ID")
	}
	if strings.TrimSpace(c.AuthToken) == "" {
		return fmt.Errorf("missing required daemon PAT (set DAEMON_PAT env var or use reliant-tools login)")
	}
	if strings.TrimSpace(c.GRPCURL) == "" {
		return fmt.Errorf("missing required RELIANT_DAEMON_GRPC_URL")
	}
	if _, err := url.Parse(c.GRPCURL); err != nil {
		return fmt.Errorf("invalid RELIANT_DAEMON_GRPC_URL: %w", err)
	}
	switch c.TLSMode {
	case TLSModeTLS, TLSModeInsecureTLSSkipVerify, TLSModeH2C:
		return nil
	default:
		return fmt.Errorf("invalid RELIANT_DAEMON_TLS_MODE %q", c.TLSMode)
	}
}
