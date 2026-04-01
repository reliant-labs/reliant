// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// ExternalClientConfig holds configuration for connecting to an external Temporal server.
type ExternalClientConfig struct {
	Host      string // Temporal frontend host (e.g. "temporal", "localhost")
	Port      int    // Temporal frontend port (e.g. 7233)
	Namespace string // Temporal namespace (e.g. "reliant")
	LogLevel  string // "silent", "debug", "info", "warn", "error" — defaults to slog.Default()
}

// NewExternalClient creates a Temporal SDK client connected to an external Temporal server.
// This is used by the standalone api-server and temporal-worker services.
// It uses the same FlexibleDataConverter and keepalive settings as the embedded client.
func NewExternalClient(ctx context.Context, cfg ExternalClientConfig) (client.Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("temporal host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 7233
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "reliant"
	}

	hostPort := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	opts := client.Options{
		HostPort:      hostPort,
		Namespace:     cfg.Namespace,
		DataConverter: NewFlexibleDataConverter(),
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{
				grpc.WithKeepaliveParams(keepalive.ClientParameters{
					Time:                30 * time.Second,
					Timeout:             10 * time.Second,
					PermitWithoutStream: true,
				}),
			},
		},
	}

	// Configure logger.
	if cfg.LogLevel == "silent" {
		nopHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 10})
		opts.Logger = log.NewStructuredLogger(slog.New(nopHandler))
	} else {
		opts.Logger = log.NewStructuredLogger(slog.Default())
	}

	return client.Dial(opts)
}
