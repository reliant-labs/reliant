// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
)

// RouterDriver identifies which daemon router backend to use.
type RouterDriver string

const (
	RouterDriverLocal RouterDriver = "local" // In-process (monolith)
	RouterDriverNATS  RouterDriver = "nats"  // NATS pub/sub (distributed)
)

// ParseRouterDriver parses a raw string into a RouterDriver.
func ParseRouterDriver(raw string) (RouterDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RouterDriverLocal):
		return RouterDriverLocal, nil
	case string(RouterDriverNATS):
		return RouterDriverNATS, nil
	default:
		return "", fmt.Errorf("invalid TRACKER_DRIVER %q (expected local or nats)", raw)
	}
}

// RouterConfig holds configuration for the daemon router.
type RouterConfig struct {
	Driver   RouterDriver
	NATSConn *nats.Conn              // Required when Driver == RouterDriverNATS
	Local    DaemonConnectionManager // Required when Driver == RouterDriverLocal
}

// NewDaemonRouter creates a DaemonRouter based on config.
func NewDaemonRouter(cfg RouterConfig) (DaemonRouter, error) {
	switch cfg.Driver {
	case RouterDriverLocal, "":
		if cfg.Local == nil {
			return nil, fmt.Errorf("local DaemonConnectionManager required for local router driver")
		}
		return NewLocalDaemonRouter(cfg.Local), nil
	case RouterDriverNATS:
		if cfg.NATSConn == nil {
			return nil, fmt.Errorf("NATS connection required for nats router driver")
		}
		return NewNATSDaemonRouter(cfg.NATSConn), nil
	default:
		return nil, fmt.Errorf("unknown router driver: %q", cfg.Driver)
	}
}
