// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"io"
	"log/slog"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// NewClient creates a Temporal SDK client connected to the embedded server
func (s *EmbeddedServer) NewClient(ctx context.Context, namespace string) (client.Client, error) {
	opts := client.Options{
		Namespace: namespace,
	}

	// Set client logger based on server's log level for consistency
	if s.config != nil {
		if s.config.LogLevel == "silent" {
			// Create a no-op slog logger (discard all output)
			nopHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 10})
			opts.Logger = log.NewStructuredLogger(slog.New(nopHandler))
		} else {
			// Use slog.Default() which inherits the app's configured log level
			// This ensures Temporal client logs respect our logging.GetLogLevel() settings
			opts.Logger = log.NewStructuredLogger(slog.Default())
		}
	}

	return s.NewClientWithOptions(ctx, opts)
}

// NewClientWithOptions allows custom client options
// Note that options.HostPort will always be overridden to connect to the embedded server
func (s *EmbeddedServer) NewClientWithOptions(ctx context.Context, opts client.Options) (client.Client, error) {
	opts.HostPort = s.frontendHostPort

	// Use flexible data converter so proto-encoded activity outputs can be
	// decoded into non-proto targets (e.g. map[string]interface{}).
	if opts.DataConverter == nil {
		opts.DataConverter = NewFlexibleDataConverter()
	}

	// Configure gRPC connection options for better reliability
	// These help detect and recover from stale connections
	opts.ConnectionOptions = client.ConnectionOptions{
		DialOptions: []grpc.DialOption{
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				// Send keepalive pings every 30 seconds if no activity
				Time: 30 * time.Second,
				// Wait 10 seconds for ping ack before considering connection dead
				Timeout: 10 * time.Second,
				// Send keepalive pings even without active streams
				PermitWithoutStream: true,
			}),
		},
	}

	return client.Dial(opts)
}
