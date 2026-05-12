// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/serverworker"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run cloud server components",
		Long: `Run Reliant cloud server components. Each subcommand starts a specific
server role for split-deployment mode.`,
	}

	cmd.AddCommand(newServerAPICmd())
	cmd.AddCommand(newServerWorkerCmd())
	cmd.AddCommand(newServerGatewayCmd())

	return cmd
}

// serverEnvOrDefault returns the environment variable value or the default.
// Local to this file to avoid modifying root.go.
func serverEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// serverEnvOrDefaultInt returns the environment variable as int, or the default.
func serverEnvOrDefaultInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// serverEnvOrDefaultBool returns the environment variable as bool, or the default.
func serverEnvOrDefaultBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}

func newServerAPICmd() *cobra.Command {
	opts := serverapi.Options{}

	cmd := &cobra.Command{
		Use:   "api",
		Short: "Run the stateless HTTP + gRPC API server",
		Long: `Starts the Reliant API server for cloud deployments. This is a stateless
server that handles HTTP REST and gRPC/ConnectRPC requests, connecting to
external Temporal, Postgres, and NATS services.

Designed to run as N replicas behind a load balancer.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse CORS origins: "*" → wildcard, otherwise comma-separated
			corsRaw, _ := cmd.Flags().GetString("cors-origins")
			if corsRaw == "*" {
				opts.CORSAllowedOrigins = []string{"*"}
			} else {
				opts.CORSAllowedOrigins = strings.Split(corsRaw, ",")
			}

			// Parse allowed email domains (empty = allow all)
			if raw := serverEnvOrDefault("ALLOWED_EMAIL_DOMAINS", ""); raw != "" {
				opts.AllowedEmailDomains = strings.Split(raw, ",")
			}

			return serverapi.Run(cmd.Context(), opts)
		},
	}

	// Ports
	cmd.Flags().IntVar(&opts.GRPCPort, "grpc-port", serverEnvOrDefaultInt("GRPC_PORT", 9090), "gRPC/ConnectRPC listen port")
	cmd.Flags().IntVar(&opts.PprofPort, "pprof-port", serverEnvOrDefaultInt("PPROF_PORT", 6060), "pprof debug server port")
	cmd.Flags().StringVar(&opts.BindAddress, "bind-address", serverEnvOrDefault("BIND_ADDRESS", "0.0.0.0"), "Network address to bind to")
	cmd.Flags().IntVar(&opts.HealthPort, "health-port", serverEnvOrDefaultInt("HEALTH_PORT", 8081), "Health/readiness HTTP endpoint port")

	// Database
	cmd.Flags().StringVar(&opts.DatabaseDriver, "db-driver", serverEnvOrDefault("DATABASE_DRIVER", "postgres"), "Database driver (postgres)")
	cmd.Flags().StringVar(&opts.DatabaseURL, "db-url", serverEnvOrDefault("DATABASE_URL", ""), "Database connection URL (required)")
	cmd.Flags().StringVar(&opts.DataDir, "data-dir", serverEnvOrDefault("DATA_DIR", "./data"), "Data directory for logs and certs")

	// Temporal
	cmd.Flags().StringVar(&opts.TemporalHost, "temporal-host", serverEnvOrDefault("TEMPORAL_HOST", "localhost"), "Temporal server host")
	cmd.Flags().IntVar(&opts.TemporalPort, "temporal-port", serverEnvOrDefaultInt("TEMPORAL_PORT", 7233), "Temporal server port")
	cmd.Flags().StringVar(&opts.TemporalNamespace, "temporal-namespace", serverEnvOrDefault("TEMPORAL_NAMESPACE", "reliant"), "Temporal namespace")

	// NATS / streaming
	cmd.Flags().StringVar(&opts.NATSURL, "nats-url", serverEnvOrDefault("NATS_URL", ""), "NATS server URL (required)")
	cmd.Flags().StringVar(&opts.StreamingDriver, "streaming-driver", serverEnvOrDefault("STREAMING_DRIVER", "nats"), "Streaming driver (memory or nats)")

	// CORS — default to wildcard; hosted deployments override via CORS_ALLOWED_ORIGINS
	corsDefault := "*"
	cmd.Flags().String("cors-origins", serverEnvOrDefault("CORS_ALLOWED_ORIGINS", corsDefault), "Comma-separated CORS allowed origins, or * for all")

	// TLS
	cmd.Flags().StringVar(&opts.TLSCertFile, "tls-cert", serverEnvOrDefault("TLS_CERT_FILE", ""), "TLS certificate file path")
	cmd.Flags().StringVar(&opts.TLSKeyFile, "tls-key", serverEnvOrDefault("TLS_KEY_FILE", ""), "TLS key file path")
	cmd.Flags().BoolVar(&opts.DisableTLS, "disable-tls", serverEnvOrDefaultBool("DISABLE_TLS", false), "Disable TLS (use plaintext HTTP)")

	// JWT
	cmd.Flags().StringVar(&opts.JWTPublicKey, "jwt-public-key", serverEnvOrDefault("JWT_PUBLIC_KEY", ""), "JWT public key PEM for token validation")
	cmd.Flags().StringVar(&opts.JWTPublicKeyFile, "jwt-public-key-file", serverEnvOrDefault("JWT_PUBLIC_KEY_FILE", ""), "Path to JWT public key PEM file")
	cmd.Flags().StringVar(&opts.JWKSURL, "jwks-url", serverEnvOrDefault("RELIANT_JWKS_URL", ""), "JWKS endpoint URL for JWT validation (alternative to PEM key)")

	return cmd
}

func newServerWorkerCmd() *cobra.Command {
	opts := serverworker.Options{}

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Temporal workflow worker",
		Long: `Starts a Temporal worker that processes workflow executions. Connects to
an external Temporal server and executes workflow activities (LLM inference,
tool execution routing, etc.).

Designed to run as N replicas for horizontal scaling.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serverworker.Run(cmd.Context(), opts)
		},
	}

	// Database
	cmd.Flags().StringVar(&opts.DatabaseDriver, "db-driver", serverEnvOrDefault("DATABASE_DRIVER", "postgres"), "Database driver (postgres)")
	cmd.Flags().StringVar(&opts.DatabaseURL, "db-url", serverEnvOrDefault("DATABASE_URL", ""), "Database connection URL (required)")
	cmd.Flags().StringVar(&opts.DataDir, "data-dir", serverEnvOrDefault("DATA_DIR", "./data"), "Data directory for logs")

	// Temporal
	cmd.Flags().StringVar(&opts.TemporalHost, "temporal-host", serverEnvOrDefault("TEMPORAL_HOST", "localhost"), "Temporal server host")
	cmd.Flags().IntVar(&opts.TemporalPort, "temporal-port", serverEnvOrDefaultInt("TEMPORAL_PORT", 7233), "Temporal server port")
	cmd.Flags().StringVar(&opts.TemporalNamespace, "temporal-namespace", serverEnvOrDefault("TEMPORAL_NAMESPACE", "reliant"), "Temporal namespace")

	// NATS / streaming
	cmd.Flags().StringVar(&opts.NATSURL, "nats-url", serverEnvOrDefault("NATS_URL", ""), "NATS server URL (required)")
	cmd.Flags().StringVar(&opts.StreamingDriver, "streaming-driver", serverEnvOrDefault("STREAMING_DRIVER", "nats"), "Streaming driver (memory or nats)")

	// Health
	cmd.Flags().IntVar(&opts.HealthPort, "health-port", serverEnvOrDefaultInt("HEALTH_PORT", 8081), "Health check endpoint port")

	return cmd
}

func newServerGatewayCmd() *cobra.Command {
	opts := servergateway.Options{}

	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the daemon connection gateway",
		Long: `Starts the daemon gateway that manages bidirectional gRPC streams to
tools-daemon processes. Routes tool execution requests from API servers
and Temporal workers to the correct daemon via NATS.

Designed to run as few stateful replicas.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return servergateway.Run(cmd.Context(), opts)
		},
	}

	// Ports
	cmd.Flags().IntVar(&opts.ToolsDaemonPort, "daemon-port", serverEnvOrDefaultInt("TOOLS_DAEMON_PORT", 9190), "Daemon bidi-streaming gRPC listen port")
	cmd.Flags().IntVar(&opts.HealthPort, "health-port", serverEnvOrDefaultInt("HEALTH_PORT", 8080), "Health/readiness HTTP endpoint port")
	cmd.Flags().StringVar(&opts.BindAddress, "bind-address", serverEnvOrDefault("BIND_ADDRESS", "0.0.0.0"), "Network address to bind to")

	// Database
	cmd.Flags().StringVar(&opts.DatabaseDriver, "db-driver", serverEnvOrDefault("DATABASE_DRIVER", "postgres"), "Database driver (postgres)")
	cmd.Flags().StringVar(&opts.DatabaseURL, "db-url", serverEnvOrDefault("DATABASE_URL", ""), "Database connection URL (required)")
	cmd.Flags().StringVar(&opts.DataDir, "data-dir", serverEnvOrDefault("DATA_DIR", "./data"), "Data directory for logs and certs")

	// NATS
	cmd.Flags().StringVar(&opts.NATSURL, "nats-url", serverEnvOrDefault("NATS_URL", ""), "NATS server URL (required)")

	// TLS
	cmd.Flags().StringVar(&opts.TLSCertFile, "tls-cert", serverEnvOrDefault("TLS_CERT_FILE", ""), "TLS certificate file path")
	cmd.Flags().StringVar(&opts.TLSKeyFile, "tls-key", serverEnvOrDefault("TLS_KEY_FILE", ""), "TLS key file path")
	cmd.Flags().BoolVar(&opts.DisableTLS, "disable-tls", serverEnvOrDefaultBool("DISABLE_TLS", false), "Disable TLS (use plaintext HTTP)")

	return cmd
}
