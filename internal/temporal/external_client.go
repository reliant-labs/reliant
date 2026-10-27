// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/observe/temporalotel"
	"github.com/reliant-labs/reliant/internal/instanceid"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
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

	// PayloadStore holds claim-checked payload blobs (see claimcheck). Every
	// process that reads or writes the namespace's histories must set it to
	// the same store, or it cannot decode what the others wrote. Nil installs
	// the codec without a store: nothing is offloaded, and decoding a
	// claim-check reference fails with claimcheck.ErrNoStore.
	PayloadStore claimcheck.Store
}

// EffectiveNamespace is the namespace used for both dialing and exact-history
// readers. An omitted/empty namespace preserves the established reliant default.
func EffectiveNamespace(namespace string) string {
	if namespace == "" {
		return "reliant"
	}
	return namespace
}

// NewExternalClient creates a Temporal SDK client connected to an external Temporal server.
// This is used by the standalone api-server and temporal-worker services.
// It uses the same FlexibleDataConverter and keepalive settings as the embedded client.
func NewExternalClient(ctx context.Context, cfg ExternalClientConfig) (client.Client, error) {
	opts, err := externalClientOptions(cfg)
	if err != nil {
		return nil, err
	}
	return client.DialContext(ctx, opts)
}

// externalClientOptions is NewExternalClient's client.Options, split out so a
// test can check them without a Temporal server.
func externalClientOptions(cfg ExternalClientConfig) (client.Options, error) {
	if cfg.Host == "" {
		return client.Options{}, fmt.Errorf("temporal host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 7233
	}
	cfg.Namespace = EffectiveNamespace(cfg.Namespace)

	hostPort := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	opts := client.Options{
		HostPort:  hostPort,
		Namespace: cfg.Namespace,
		// The SDK default identity is "<pid>@<hostname>", and the hostname in
		// it is not stable: macOS reports the same machine as *.local or *.lan
		// depending on how it is currently reachable. A LastWorkerIdentity that
		// changes on a network event reads as "the worker moved hosts" when
		// nothing moved — that misread happened during a real incident. The
		// instance id makes the machine segment stable while keeping the pid
		// and hostname that made the default readable.
		Identity:      instanceid.WorkerIdentity(),
		DataConverter: NewFlexibleDataConverter(WithPayloadStore(cfg.PayloadStore)),
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
		opts.Logger = NewSDKLogger(slog.Default())
	}

	// One interceptor on the client traces both sides: StartWorkflow and
	// SignalWithStart inject the caller's span into the workflow header, and
	// every worker built from this client continues it, so the request, the
	// workflow and its activities are one trace.
	tracing, err := temporalotel.NewInterceptor(temporalotel.Options{})
	if err != nil {
		return client.Options{}, err
	}
	opts.Interceptors = []interceptor.ClientInterceptor{tracing}

	return opts, nil
}
