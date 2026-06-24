// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// RouterDriver identifies which daemon router backend to use.
type RouterDriver string

const (
	RouterDriverNATS RouterDriver = "nats" // NATS pub/sub (distributed)
)

// ParseRouterDriver parses a raw string into a RouterDriver.
func ParseRouterDriver(raw string) (RouterDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RouterDriverNATS):
		return RouterDriverNATS, nil
	default:
		return "", fmt.Errorf("invalid TRACKER_DRIVER %q (expected nats)", raw)
	}
}

// RouterConfig holds configuration for the daemon router.
type RouterConfig struct {
	Driver             RouterDriver
	NATSConn           *nats.Conn                                   // Required when Driver == RouterDriverNATS
	DB                 db.Repository                                // Optional: enables fast DB-based daemon online check for NATS router
	ControlPlaneClient reliantv1connect.DaemonRegistryServiceClient // Optional: gRPC client for control plane daemon resolution
}

// NewDaemonRouter creates a DaemonRouter based on config.
func NewDaemonRouter(cfg RouterConfig) (DaemonRouter, error) {
	switch cfg.Driver {
	case RouterDriverNATS, "":
		if cfg.NATSConn == nil {
			return nil, fmt.Errorf("NATS connection required for nats router driver")
		}
		var opts []NATSRouterOption
		if cfg.DB != nil {
			opts = append(opts, WithDatabase(cfg.DB))
		}
		if cfg.ControlPlaneClient != nil {
			opts = append(opts, WithControlPlaneClient(cfg.ControlPlaneClient))
		}
		return NewNATSDaemonRouter(cfg.NATSConn, opts...), nil
	default:
		return nil, fmt.Errorf("unknown router driver: %q", cfg.Driver)
	}
}
